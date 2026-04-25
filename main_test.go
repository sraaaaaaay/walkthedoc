package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type parsingTestCase struct {
	name, text string
	wantResult []string
}

type validityTestCase struct {
	name, text string
	wantValid  bool
}

type httpTestCase struct {
	name      string
	status    int
	wantValid bool
}

func TestGetLineUrls(t *testing.T) {
	// Arrange
	tests := []parsingTestCase{
		{
			name:       "single https URL",
			text:       `Check out [this](https://example.com) link`,
			wantResult: []string{"https://example.com"},
		},
		{
			name:       "single https URL with tooltip",
			text:       `Check out [this](https://example.com "Hello there") link`,
			wantResult: []string{"https://example.com"},
		},
		{
			name:       "bare GFM-style http URL",
			text:       `Visit http://example.com for details`,
			wantResult: []string{"http://example.com"},
		},
		{
			name:       "multiple bare GFM-style URLs",
			text:       `See https://one.com and https://two.com/path`,
			wantResult: []string{"https://one.com", "https://two.com/path"},
		},
		{
			name:       "autolink - angle brackets",
			text:       `<https://example.com/page>`,
			wantResult: []string{"https://example.com/page"},
		},
		{
			name:       "URL with square bracket",
			text:       `[https://example.com/page]`,
			wantResult: []string{"https://example.com/page"},
		},
		{
			name:       "URL with round bracket",
			text:       `(https://example.com/page)`,
			wantResult: []string{"https://example.com/page"},
		},
		{
			name:       "no URLs",
			text:       `According to all known laws of aviation`,
			wantResult: []string{},
		},
		{
			name:       "empty text",
			text:       "",
			wantResult: []string{},
		},
	}

	// Act
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := getLineUrls([]byte(testCase.text))

			// Assert
			if len(got) != len(testCase.wantResult) {
				t.Fatalf("found %d URLs in the line, but there were %d", len(got), len(testCase.wantResult))
			}
			for i := range got {
				if string(got[i]) != testCase.wantResult[i] {
					t.Errorf("parsed URL %q, but wanted %q", got[i], testCase.wantResult[i])
				}
			}
		})
	}
}

func TestProcessLineMarkdownRefs_JekyllMode(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.md"), []byte("# Other"), 0644); err != nil {
		t.Fatal("error while writing markdown file into temporary directory")
	}

	jekyllModeFlag = true
	tests := []validityTestCase{
		{
			name:      "link with .md suffix is invalid",
			text:      `[Other](other.md)`,
			wantValid: false,
		},
		{
			name:      "extensionless link resolves to .md file",
			text:      `[Other](other)`,
			wantValid: true,
		},
		// TODO change this if we ever start resolving fragments
		{
			name:      "anchor style link resolves to .md file",
			text:      `[Other](other#section)`,
			wantValid: true,
		},
	}

	// Act
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			srcFile := filepath.Join(dir, "source.md")
			w := walker{seenResources: make(map[string]struct{})}
			results := make(chan result, 1)

			w.processLineMarkdownRefs(srcFile, []byte(testCase.text), 1, results)
			w.validatingLinesDone.Wait()
			close(results)

			r, ok := <-results

			// Assert
			if !ok {
				t.Fatal("expected to receive a result from the channel")
			}
			if r.isValid != testCase.wantValid {
				t.Errorf("result validity was %v, but wanted %v", r.isValid, testCase.wantValid)
			}
		})
	}
}

func TestProcessLineMarkdownRefs_NonJekyllMode(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.md"), []byte("# Other"), 0644); err != nil {
		t.Fatal("error while writing markdown file into temporary directory")
	}

	jekyllModeFlag = false
	tests := []validityTestCase{
		{
			name:      "single .md file is valid",
			text:      `[Other](other.md)`,
			wantValid: true,
		},
		{
			name:      "multiple .md files on the same line are valid",
			text:      `[First](other.md) and [Second](other.md)`,
			wantValid: true,
		},
		{
			name:      "extensionless link is always invalid",
			text:      `[Other](other)`,
			wantValid: false,
		},
	}

	// Act
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			srcFile := filepath.Join(dir, "source.md")
			w := walker{seenResources: make(map[string]struct{})}
			results := make(chan result, 1)

			w.processLineMarkdownRefs(srcFile, []byte(testCase.text), 1, results)
			w.validatingLinesDone.Wait()
			close(results)

			r, ok := <-results

			// Assert
			if !ok {
				t.Fatal("expected to receive a result from the channel")
			}
			if r.isValid != testCase.wantValid {
				t.Errorf("result validity was %v, but wanted %v", r.isValid, testCase.wantValid)
			}
		})
	}
}

func TestProcessLineMarkdownRefs_ImageRef(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "readme.md")
	if err := os.WriteFile(filepath.Join(dir, "image.png"), []byte(""), 0644); err != nil {
		t.Fatal("error while writing image file to temporary directory")
	}

	w := walker{seenResources: make(map[string]struct{})}
	jekyllModeFlag = false
	results := make(chan result, 1)

	// Act
	w.processLineMarkdownRefs(srcFile, []byte(`![screenshot](image.png)`), 1, results)
	w.validatingLinesDone.Wait()
	close(results)

	r, ok := <-results

	// Assert
	if !ok {
		t.Fatal("expected to receive a result from the channel")
	}
	if !r.isValid {
		t.Error("image.png reference was expected to be valid")
	}
}

func TestGetLineMarkdownRefs_IgnoresNonLocal(t *testing.T) {
	// Arrange
	line := `[a](https://example.com) [b](http://example.com) [c](mailto:x@y.com) [d](#heading) [e](local.md)`

	// Act
	refs := getLineMarkdownRefs([]byte(line))

	// Assert
	if len(refs) != 1 {
		t.Fatalf("found %d references, but wanted 1", len(refs))
	}
	if string(refs[0]) != "local.md" {
		t.Errorf("found a reference to %q, but wanted %q", refs[0], "local.md")
	}
}

func TestValidateUrl_Head405IsValid(t *testing.T) {
	// Arrange
	numHead := 0
	numGet := 0
	testServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				numHead++
				w.WriteHeader(http.StatusMethodNotAllowed)
			}

			if r.Method == http.MethodGet {
				numGet++
				w.WriteHeader(http.StatusOK)
			}
		}))
	defer testServer.Close()

	w := walker{
		httpClient:       *http.DefaultClient,
		perHostSemaphore: make(map[string]chan struct{}),
		seenResources:    make(map[string]struct{}),
	}

	results := make(chan result, 1)

	// Act
	w.validateUrl("fake.md", testServer.URL, 1, results)

	// Assert
	r, ok := <-results
	if !ok {
		t.Fatal("expected a result from the channel")
	}
	if numHead != 1 {
		t.Errorf("expected 1 HEAD request, got %d", numHead)
	}
	if numGet != 0 {
		t.Errorf("expected 0 GET requests, got %d", numGet)
	}
	if !r.isValid {
		t.Error("HEAD 405 should be treated as valid")
	}
}

func TestValidateUrl_StatusGating(t *testing.T) {
	// Arrange
	tests := []httpTestCase{
		{name: "200 OK is valid", status: http.StatusOK, wantValid: true},
		{name: "403 Forbidden is valid", status: http.StatusForbidden, wantValid: true},                       // could be anti-bot
		{name: "500 Internal Server Error is valid", status: http.StatusInternalServerError, wantValid: true}, // could be transient
		{name: "404 Not Found is invalid", status: http.StatusNotFound, wantValid: false},                     // definitely invalid
		{name: "410 Gone is invalid", status: http.StatusGone, wantValid: false},                              // definitely invalid
	}

	// Act
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			testServer := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(testCase.status)
				}))
			defer testServer.Close()

			w := walker{
				httpClient:       *http.DefaultClient,
				perHostSemaphore: make(map[string]chan struct{}),
				seenResources:    make(map[string]struct{}),
			}
			results := make(chan result, 1)

			w.validateUrl("fake.md", testServer.URL, 1, results)

			// Assert
			r, ok := <-results
			if !ok {
				t.Fatal("expected a result from the channel")
			}
			if r.isValid != testCase.wantValid {
				t.Errorf("status %d: validity was %v, but wanted %v", testCase.status, r.isValid, testCase.wantValid)
			}
		})
	}
}
