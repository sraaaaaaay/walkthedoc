package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

type walker struct {
	httpClient http.Client

	resultTypeStats     map[resourceType]stat
	perHostSemaphore    map[string]chan struct{}
	perHostSemaphoreMu  sync.Mutex
	seenResources       map[string]struct{}
	seenResourcesMu     sync.Mutex
	validatingLinesDone sync.WaitGroup
}

type result struct {
	fmt.Stringer

	link            string
	foundInFile     string
	foundLineNumber int
	resourceType    resourceType
	isValid         bool
}

type stat struct {
	valid, invalid int64
}

type resourceType uint8

const (
	unknown resourceType = iota
	markdownFile
	imageFile
	externalUrl
)

const (
	maxActiveReqsPerHost = 4
	redRgb               = "240;110;120"
	greenRgb             = "120;200;75"
	dimRgb               = "137;137;137"
)

var (
	programWalker = walker{
		httpClient: http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     3 * time.Second,
				DisableCompression:  true,
			}},
		resultTypeStats:  make(map[resourceType]stat),
		perHostSemaphore: make(map[string]chan struct{}),
		seenResources:    make(map[string]struct{}),
	}

	urlTable [256]bool

	dirFlag        string
	showAllFlag    bool
	checkUrlsFlag  bool
	jekyllModeFlag bool
	quietFlag      bool

	nonLocalPrefixes = [][]byte{
		[]byte("http://"),
		[]byte("https://"),
		[]byte("mailto:"),
		[]byte("#"),
	}
)

func init() {
	// Populate a lookup table with bytes for common link terminators (whitespace, brackets, etc)
	for _, c := range []byte(" \t\n\r\"'<>,()[]") {
		urlTable[c] = true
	}
}

func main() {
	flag.StringVar(&dirFlag, "d", ".", "(Directory) - directory to search")
	flag.BoolVar(&showAllFlag, "a", false, "(All) - print all responses")
	flag.BoolVar(&checkUrlsFlag, "l", false, "(Links) - check external URLs")
	flag.BoolVar(&quietFlag, "q", false, "(Quiet) - print only a table of results")
	flag.BoolVar(&jekyllModeFlag, "j", false, "(Jekyll mode) - validate that relative links map to existing files")
	flag.Parse()

	if _, err := os.Stat(dirFlag); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "directory not found\n")
		os.Exit(1)
	}

	// Wait for results to be produced by the directory search
	// and print them out as they arrive.
	results := make(chan result, 64)
	var printerDone sync.WaitGroup
	allValid := true
	printerDone.Go(func() {
		currentStat := stat{}
		for r := range results {

			currentStat = programWalker.resultTypeStats[r.resourceType]
			if r.isValid {
				currentStat.valid++
			} else {
				allValid = false
				currentStat.invalid++
			}
			programWalker.resultTypeStats[r.resourceType] = currentStat

			if quietFlag {
				continue
			}

			if !r.isValid || showAllFlag {
				fmt.Println(r.String())
			}
		}
	})

	// Traverse the current directory, scanning the contents of any Markdown files.
	// Check that any references to .md files exist, or if enabled, send a HTTP HEAD
	// to a URL to check it responds.
	filepath.WalkDir(dirFlag, func(path string, entry fs.DirEntry, err error) error {
		if entry.IsDir() {
			return nil
		}

		if !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineNumber := 0
		var line []byte
		for scanner.Scan() {
			lineNumber++
			line = scanner.Bytes()

			if !containsLink(line) {
				continue
			}

			if checkUrlsFlag {
				programWalker.processLineUrls(path, line, lineNumber, results)
			}

			programWalker.processLineMarkdownRefs(path, line, lineNumber, results)
		}
		return nil
	})

	programWalker.validatingLinesDone.Wait()
	close(results)
	printerDone.Wait()

	table := tabwriter.NewWriter(os.Stdout, 0, 0, 6, ' ', 0)
	defer table.Flush()
	fmt.Fprintln(table, "")
	fmt.Fprintln(table, fmt.Sprintf("Reference Type\tTotal\tErrors"))
	writeStatsRow(table, "Markdown file", programWalker.resultTypeStats[markdownFile])
	writeStatsRow(table, "Image file", programWalker.resultTypeStats[imageFile])
	writeStatsRow(table, "External URL", programWalker.resultTypeStats[externalUrl])
	writeStatsRow(table, "Uncategorised", programWalker.resultTypeStats[unknown])

	if checkUrlsFlag {
		hostNames := make([]string, len(programWalker.perHostSemaphore))
		i := 0
		for key, _ := range programWalker.perHostSemaphore {
			hostNames[i] = key
			i++
		}
		sort.Strings(hostNames)
		fmt.Fprintln(table, "")
		fmt.Fprintln(table, "Checked hostnames:", hostNames)
	}

	if !allValid {
		os.Exit(1)
	}
}

func writeStatsRow(t *tabwriter.Writer, resourceType string, stats stat) {
	fmt.Fprintln(t, fmt.Sprintf("%s\t%5d\t%6d", resourceType, stats.valid+stats.invalid, stats.invalid))
}

func (w *walker) processLineUrls(currentFile string, line []byte, lineNumber int, results chan<- result) {
	urlLinks := getLineUrls(line)
	for _, urlBytes := range urlLinks {
		// Mark the URL as seen
		urlStr := string(urlBytes)
		if !w.markSeen(urlStr) {
			continue
		}

		w.validatingLinesDone.Go(func() {
			w.validateUrl(currentFile, urlStr, lineNumber, results)
		})
	}
}

func (w *walker) validateUrl(currentFile, foundUrl string, lineNumber int, results chan<- result) {
	parsedUrl, parseErr := url.Parse(foundUrl)
	if parseErr != nil {
		return
	}

	// Set up a throttle for the hostname - we don't want to
	// hammer any smaller sites with concurrent requests
	hostname := parsedUrl.Hostname()
	w.perHostSemaphoreMu.Lock()
	if _, ok := w.perHostSemaphore[hostname]; !ok {
		w.perHostSemaphore[hostname] = make(chan struct{}, maxActiveReqsPerHost)
	}

	// Capture a "local" of the channel for use within this goroutine
	sem := w.perHostSemaphore[hostname]
	w.perHostSemaphoreMu.Unlock()

	// The channel is buffered - sending will block
	// until the concurrent requests are below the set limit
	sem <- struct{}{}
	defer func() { <-sem }()

	headReq, headErr := createRequest(http.MethodHead, foundUrl)
	if headErr != nil {
		return
	}

	isValid := false
	headResp, headReqErr := w.httpClient.Do(headReq)
	if headReqErr == nil {
		// Treat any reachable host as valid. Transient 400-500
		// errors might not happen in a browser, so avoid false
		// positives. 404/410 are exceptions as we know with some
		// certainty that the resource really isn't there.
		isValid = headResp.StatusCode != http.StatusNotFound && headResp.StatusCode != http.StatusGone
		headResp.Body.Close()
	}

	results <- result{
		link:            foundUrl,
		foundInFile:     currentFile,
		foundLineNumber: lineNumber,
		resourceType:    externalUrl,
		isValid:         isValid,
	}
}

func createRequest(method, to string) (*http.Request, error) {
	req, err := http.NewRequest(method, to, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Walk_The_Doc/v1")
	return req, nil
}

func getLineUrls(line []byte) [][]byte {
	links := make([][]byte, 0)

	// The line might contain multiple hyperlinks, so we need to loop over and trim
	// the contents down until there's nothing left
	for {
		httpIdx := bytes.Index(line, []byte("http://"))
		httpsIdx := bytes.Index(line, []byte("https://"))

		startIdx := httpsIdx
		if httpsIdx == -1 || (httpIdx != -1 && httpIdx < httpsIdx) {
			startIdx = httpIdx
		}

		if startIdx == -1 {
			break
		}

		remaining := line[startIdx:]
		endIdx := findUrlEnd(remaining)
		links = append(links, remaining[:endIdx])
		line = remaining[endIdx:]
	}

	return links
}

func (w *walker) processLineMarkdownRefs(currentFile string, line []byte, lineNumber int, results chan<- result) {
	mdLinks := getLineMarkdownRefs(line)
	for _, refBytes := range mdLinks {
		refStr := string(refBytes)
		if !w.markSeen(refStr) {
			continue
		}

		w.validatingLinesDone.Go(func() {
			ext := filepath.Ext(refStr)
			referencedFileName := refStr
			referencedResourceType := unknown

			switch ext {
			case ".md":
				referencedResourceType = markdownFile
			case ".png", ".jpg", ".jpeg", ".gif", ".svg":
				referencedResourceType = imageFile
			}

			// If we found "foo" in Jekyll mode, test if the file foo.md exists.
			// If we found "foo.md" in Jekyll mode, it's invalid, so just test foo.md.md to fail it.
			if jekyllModeFlag && (ext == "" || ext == ".md") {
				referencedFileName = refStr + ".md"
				referencedResourceType = markdownFile
			}

			_, err := os.Stat(filepath.Join(filepath.Dir(currentFile), referencedFileName))
			results <- result{
				link:            refStr,
				foundInFile:     currentFile,
				foundLineNumber: lineNumber,
				resourceType:    referencedResourceType,
				isValid:         err == nil,
			}
		})
	}
}

func (w *walker) markSeen(key string) bool {
	w.seenResourcesMu.Lock()
	defer w.seenResourcesMu.Unlock()
	if _, seen := w.seenResources[key]; seen {
		return false
	}
	w.seenResources[key] = struct{}{}
	return true
}

func getLineMarkdownRefs(line []byte) [][]byte {
	var foundLinks [][]byte

	for {
		// Cut out the surrounding symbols from the formatting
		_, after, found := bytes.Cut(line, []byte("]("))
		if !found {
			break
		}

		ref, after, found := bytes.Cut(after, []byte(")"))
		if !found {
			break
		}

		line = after

		if isNonLocalRef(ref) {
			continue
		}

		// Remove header links - just check the file.
		ref, _, _ = bytes.Cut(ref, []byte("#"))
		foundLinks = append(foundLinks, ref)
	}

	return foundLinks
}

func isNonLocalRef(ref []byte) bool {
	if len(ref) == 0 {
		return true
	}
	for _, prefix := range nonLocalPrefixes {
		if bytes.HasPrefix(ref, prefix) {
			return true
		}
	}
	return false
}

func (r *result) String() string {
	label := ""
	switch r.resourceType {
	case markdownFile:
		label = "[Markdown File]"
	case imageFile:
		label = "[Image File]"
	case externalUrl:
		label = "[External URL]"
	default:
		label = "[Uncategorised]"
	}

	colour := redRgb
	if r.isValid {
		colour = greenRgb
	}

	paddedLabel := formatSgr(fmt.Sprintf("%-15s", label), colour)
	paddedLink := fmt.Sprintf("%-30s", r.link)
	lineInfo := formatSgr(fmt.Sprintf("(%s, line %d)", r.foundInFile, r.foundLineNumber), dimRgb)

	return fmt.Sprintf("%s %s %s", paddedLabel, paddedLink, lineInfo)
}

func containsLink(line []byte) bool {
	return bytes.Contains(line, []byte("http")) || bytes.Contains(line, []byte("]("))
}

func findUrlEnd(s []byte) int {
	for i := range s {
		if urlTable[s[i]] {
			return i
		}
	}
	return len(s)
}

func formatSgr(s string, ansiRgb string) string {
	return fmt.Sprintf("%c[38;2;%sm%s%c[0m", 0x1b, ansiRgb, s, 0x1b)
}
