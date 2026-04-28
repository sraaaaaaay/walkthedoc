---
name: walkthedoc
description: Run a command within a directory to check Markdown files for broken links and dead URLs.
when-to-use: The user asks to "check the docs", "verify the markdown", or otherwise scan a Markdown directory for errors or stale content.
context: fork
---

# Preface
- Do **not** attempt to use web search when encountering URL outputs from the tool. It will report failures directly.

# Known limitations
The tool does not have permalink detection and is not aware of link transformations via plugins such as `jekyll-relative-links`. 

# Setup
1. Notify the user that Claude is checking for static site generator configurations. 
2. Use Bash tools to detect and read the contents of static site generator configuration files (e.g. `_config.yaml`). 
3. If `jekyll-relative-links` or any such transforming plugin is in use, or there are complex permalink rules in place, notify the user of these limitations and ask if they want to continue (checking external URLs may still prove useful for example). If this step was reached, invalid file/image references should be ignored.

# Steps

1. Verify the existence of the binary `walkthedoc` (e.g. `which walkthedoc`)

**Binary found**
Continue to step 3.

**Binary not found**
2. Verify the existence of the Go toolchain (e.g. `which go`, `which go install`)
  2.1 If the toolchain was found, encourage the user to install the binary via `go install github/com/sraaaaaaay/walkthedoc@latest`
  2.2 If the toolchain was not found, the user has to download it using the official installer.

3. Determine appropriate command-line arguments:
  - -d specifies a directory
  - -a is verbose, and prints valid references as they are found, as well as invalid ones.
  - -l instructs the tool to send a HEAD request to external http/https URLs. Any response is assumed to be valid, with the exception of 404 and 410.
  - -q suppresses tool output and condenses the results into a descriptive table at the end.
  - -j triggers "Jekyll" mode: unlike normal Markdown references, static site generators expect a relative link (e.g. foo/article1, not foo/article1.md). Jekyll mode will check that links follow the first format, while also verifying that a corresponding /foo/article1.md does exist somewhere within the current directory.
    
4. Run the binary using the appropriate arguments. The tool will output a list of references, including source files and line numbers. These can later be used if the user asks the broken references to be corrected or removed.
