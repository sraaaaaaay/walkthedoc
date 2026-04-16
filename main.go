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
	"strings"
	"sync"
	"time"
)

const (
	maxActiveReqsPerHost = 2
)

var (
	httpClient = http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     10 * time.Second,
			DisableCompression:  true,
		}}

	perHostSemaphore   = make(map[string]chan struct{})
	perHostSemaphoreMu sync.Mutex

	seenUrls   = make(map[string]struct{})
	seenUrlsMu sync.Mutex

	waitGroup sync.WaitGroup

	redAnsi   = 31
	greenAnsi = 32
	urlTable  [256]bool

	dirFlag       string
	showAllFlag   bool
	checkUrlsFlag bool
)

func init() {
	// Populate a lookup table with bytes for common link terminators (whitespace, brackets, etc)
	for _, c := range []byte(" \t\n\r\"'<>,()") {
		urlTable[c] = true
	}
}

// TODO
// - Rate limiting?
// - Save successful ones to 24hr cache to reduce spam
func main() {
	flag.StringVar(&dirFlag, "d", ".", "directory to search")
	flag.BoolVar(&showAllFlag, "a", false, "print all HTTP responses")
	flag.BoolVar(&checkUrlsFlag, "l", false, "check external URLs")
	flag.Parse()

	defer waitGroup.Wait()

	if _, err := os.Stat(dirFlag); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "directory not found\n")
			os.Exit(1)
		}
	}

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

		scanner := bufio.NewScanner(file)
		lineNumber := 0
		var line []byte
		for scanner.Scan() {
			lineNumber++
			line = scanner.Bytes()

			if !containsLink(line) {
				continue
			}

			getInvalidUrls(line, path, lineNumber)
			getInvalidMarkdownRefs(line, path, lineNumber)
		}

		return nil
	})
}

func getInvalidUrls(line []byte, path string, lineNumber int) {
	links := getLineUrls(line)

	for _, urlBytes := range links {

		// Mark the URL as seen
		urlStr := string(urlBytes)
		seenUrlsMu.Lock()
		if _, seen := seenUrls[urlStr]; seen {
			seenUrlsMu.Unlock()
			continue
		}
		seenUrls[urlStr] = struct{}{}
		seenUrlsMu.Unlock()

		waitGroup.Add(1)
		defer waitGroup.Done()

		parsedUrl, parseErr := url.Parse(urlStr)
		if parseErr != nil {
			return
		}

		// Set up a throttle for the hostname - we don't want to
		// hammer any smaller sites with concurrent requests
		hostname := parsedUrl.Hostname()
		perHostSemaphoreMu.Lock()
		if _, ok := perHostSemaphore[hostname]; !ok {
			perHostSemaphore[hostname] = make(chan struct{}, maxActiveReqsPerHost)
		}

		// Capture a "local" of the channel for use within this goroutine
		sem := perHostSemaphore[hostname]
		perHostSemaphoreMu.Unlock()

		// The channel is buffered - sending will block
		// until the concurrent requests are below the set limit
		sem <- struct{}{}
		defer func() { <-sem }()

		req, err := http.NewRequest(http.MethodHead, urlStr, nil)
		if err != nil {
			return
		}

		req.Header.Set("User-Agent", "Walk_The_Doc/v1")

		response, reqErr := httpClient.Do(req)
		if reqErr != nil {
			fmt.Printf("%s %s (%s, line %d)\n", formatSgr("[Invalid]", redAnsi), urlStr, path, lineNumber)
		} else {
			if showAllFlag {
				fmt.Printf("%s %s\n", formatSgr("["+response.Status+"]", greenAnsi), urlStr)
			}
		}
	}
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

func getInvalidMarkdownRefs(line []byte, path string, lineNumber int) {
	mdLinks := getLineMarkdownRefs(line)
	for _, refBytes := range mdLinks {
		refStr := string(refBytes)
		seenUrlsMu.Lock()
		if _, seen := seenUrls[refStr]; seen {
			seenUrlsMu.Unlock()
			continue
		}
		seenUrls[refStr] = struct{}{}
		seenUrlsMu.Unlock()

		waitGroup.Add(1)
		go func(ref, sourcePath string, lineNum int) {
			defer waitGroup.Done()

			absPath := filepath.Join(filepath.Dir(sourcePath), ref)
			if _, statErr := os.Stat(absPath); statErr != nil {
				fmt.Printf("%s %s (%s, line %d)\n", formatSgr("[Invalid]", redAnsi), ref, sourcePath, lineNum)
			} else if showAllFlag {
				fmt.Printf("%s %s\n", formatSgr("[OK]", greenAnsi), ref)
			}
		}(refStr, path, lineNumber)
	}
}

func getLineMarkdownRefs(text []byte) [][]byte {
	links := make([][]byte, 0)

	for {
		idx := bytes.Index(text, []byte("]("))
		if idx == -1 {
			break
		}

		remaining := text[idx+2:]
		endIdx := bytes.IndexByte(remaining, ')')
		if endIdx == -1 {
			break
		}

		ref := remaining[:endIdx]
		text = remaining[endIdx:]

		if len(ref) == 0 ||
			bytes.HasPrefix(ref, []byte("http://")) ||
			bytes.HasPrefix(ref, []byte("https://")) ||
			bytes.HasPrefix(ref, []byte("mailto:")) ||
			bytes.HasPrefix(ref, []byte("#")) {
			continue
		}

		// Remove any fragment (header links etc)
		if fragIdx := bytes.IndexByte(ref, '#'); fragIdx != -1 {
			ref = ref[:fragIdx]
		}

		links = append(links, ref)
	}

	return links
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

func formatSgr(s string, ansiColour int) string {
	return fmt.Sprintf("%c[%dm%s%c[0m", 0x1b, ansiColour, s, 0x1b)
}
