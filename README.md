<h1 align="center">walkthedoc</h1>
<p align="center"><b><i>Check dead links, images and external URLs in Markdown files</i></b></p>
<p align="center">
    <a href="https://github.com/sraaaaaaay/walkthedoc/commits/main">
        <img src="https://img.shields.io/github/last-commit/sraaaaaaay/walkthedoc?style=flat" alt="Last Commit">
    </a>
    <a href="LICENSE">
        <img src="https://img.shields.io/github/license/sraaaaaaay/walkthedoc" alt="License">
    </a>
</p>

<p>
Walkthedoc is a simple command-line tool to scan a collection of Markdown files for "invalid" content references. An "invalid" reference is:

- A Markdown file reference such as [this](foo), where foo.md does not exist
- An image link that does not have a corresponding file.
- An external URL that gets either no response, Not Authorized (404) or Gone (410).

Walkthedoc also has support for basic command-line arguments, including:
- Target directory
- Instruction to check external URLs
- Verbose output
- Quiet mode, which summarises results in a table
- "Jekyll mode", which enables basic compatibility for link formats expected by static site generators
</p>

<h2 align="center">Usage</h2>
<p>

```
walkthedoc -d ./examples/docs
```

</p>

<h2 align="center">Claude Code plugin</h2>
<p>
Walkthedoc also includes a skill and plugin/marketplace for Claude Code. It can be installed with:

```
claude plugin marketplace add sraaaaaaay/walkthedoc && claude plugin install walkthedoc@walkthedoc
```

<b><i>...but Claude can already search files!</i></b>
|                          | Time   | Tokens |
|--------------------------|--------|--------|
| Walkthedoc               | 32s    | 5.5k   |
| File search / web search | 2m 19s | 39k    |
| *Factor*                   | *4.3x*   | *7x*     |

</p>

<h2 align="center">Future</h2>
<p>
- Better support for static site generators (permalinks, frontmatter, etc)
</p>
