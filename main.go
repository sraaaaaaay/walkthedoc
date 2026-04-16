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
	// Populate a lookup table with bytes for common url terminators (whitespace, brackets, etc)
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

	if _, err := os.Stat(dirFlag); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "directory not found\n")
			os.Exit(1)
		}
	}

	// Traverse the current directory, scanning the contents of any Markdown files.
	// If a URL (http/https) is found, send a HEAD request to verify a response.
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

			if !isHyperlinkLine(line) {
				continue
			}

			links := readHyperlinks(line)
			for _, urlBytes := range links {
				// Just store an empty struct in the map, indicating that
				// the URL has been seen
				urlStr := string(urlBytes)
				seenUrlsMu.Lock()
				if _, seen := seenUrls[urlStr]; seen {
					continue
				}
				seenUrls[urlStr] = struct{}{}
				seenUrlsMu.Unlock()

				waitGroup.Add(1)
				go func(rawUrl string, lineNum int) {
					defer waitGroup.Done()

					// If needed, set up a semaphore for the hostname - we
					// don't want to hammer any smaller sites with requests
					parsedUrl, parseErr := url.Parse(rawUrl)
					if parseErr != nil {
						return
					}

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

					req, err := http.NewRequest(http.MethodHead, rawUrl, nil)
					if err != nil {
						return
					}

					req.Header.Set("User-Agent", "Walk_The_Doc/v1")

					response, reqErr := httpClient.Do(req)
					if reqErr != nil {
						fmt.Printf("%s %s (%s, line %d)\n", formatSgr("[Invalid]", redAnsi), rawUrl, path, lineNum)
					} else {
						if showAllFlag {
							fmt.Printf("%s %s\n", formatSgr("["+response.Status+"]", greenAnsi), rawUrl)
						}
					}
				}(urlStr, lineNumber)
						}
					}
				}(url)
			}
		}

		return nil
	})

	// Wait for all requests to discovered urls to complete or timeout
	// before reporting.
	waitGroup.Wait()
}

func isHyperlinkLine(line []byte) bool {
	return bytes.Contains(line, []byte("http"))
}

func readHyperlinks(text []byte) [][]byte {
	links := make([][]byte, 0)

	// The line might contain multiple hyperlinks, so we need to loop over and trim
	// the contents down until there's nothing left
	for {
		httpIdx := bytes.Index(text, []byte("http://"))
		httpsIdx := bytes.Index(text, []byte("https://"))

		startIdx := httpsIdx
		if httpsIdx == -1 || (httpIdx != -1 && httpIdx < httpsIdx) {
			startIdx = httpIdx
		}

		if startIdx == -1 {
			break
		}

		remaining := text[startIdx:]
		endIdx := findUrlEnd(remaining)
		links = append(links, remaining[:endIdx])
		text = remaining[endIdx:]
	}

	return links
}

func findUrlEnd(s []byte) int {
	for i := 0; i < len(s); i++ {
		if urlTable[s[i]] {
			return i
		}
	}
	return len(s)
}

func formatSgr(s string, ansiColour int) string {
	return fmt.Sprintf("%c[%dm%s%c[0m", 0x1b, ansiColour, s, 0x1b)
}
