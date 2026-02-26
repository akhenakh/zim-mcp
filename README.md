# MCP ZIM Server

A Model Context Protocol (MCP) server written in Go that allows LLMs to search and read offline `.zim` archives (such as offline Wikipedia, StackOverflow, DevDocs, etc.). 

It leverages [mcp-go](https://github.com/mark3labs/mcp-go) for the protocol implementation, full-text Xapian search via [Go ZIM bindings](https://github.com/akhenakh/zim-cgo), and [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown) to convert heavy HTML articles into clean, LLM-friendly Markdown on the fly. All outputs are wrapped in structured JSON.

## Features

- **Full-Text Search (`search_zim`)**: Instantly search the ZIM archive using the built-in Xapian index. Returns the top matches with their titles, paths, and relevance scores.
- **Article Reading & Conversion (`read_article`)**: Fetches an article by its internal path and seamlessly converts its HTML contents into clean Markdown, drastically reducing token usage and improving LLM comprehension.
- **JSON Structured Output**: All tool responses are wrapped in structured JSON envelopes for maximum compatibility and predictable LLM parsing.

## Prerequisites

Because the underlying ZIM library (`libzim`) and search engine (`xapian`) are C++ libraries, this project requires `cgo` and the appropriate system dependencies.

### 1. Install System Dependencies
**Ubuntu/Debian:**
```bash
sudo apt-get install libzim-dev libxapian-dev
```

**macOS (via Homebrew):**
```bash
brew install libzim xapian
```

### 2. Download a ZIM file
You can download offline archives from the [Kiwix Library](https://library.kiwix.org/). Ensure you download a file that includes a search index.

## Installation & Build

```bash
# Build the binary
go install github.com/akhenakh/zim-mcp@latest
```

## Usage

Use `-z` flag to specify the zim file to use.

The server communicates over `stdio`, making it ideal for direct integration with MCP clients like Claude Desktop. 

### Network Mode (HTTP/SSE)

To run the server as a microservice and expose it to the network, use the -listen flag.
code Bash

```sh
mcp-zim -z /path/to/your/archive.zim -listen :4545
```

## Integration with Claude Desktop

To make this server available to Claude, add it to your Claude Desktop configuration file. 

Add the following to your configuration, replacing the paths with the absolute paths on your system:

```json
{
  "mcpServers": {
    "offline-wikipedia": {
      "command": "/absolute/path/to/mcp-zim",
      "args": [
        "-z",
        "/absolute/path/to/wikipedia_en_all_maxi_2024-01.zim"
      ]
    }
  }
}
```

Restart Claude Desktop. The tools will now be available in your chats!

## Available Tools

### `search_zim`
Searches the offline ZIM archive for articles.
- **Inputs**: 
  - `query` (string, required): The keyword or phrase to search for.
  - `count` (number, optional): Number of results to return. Defaults to 5.
- **Output**: A JSON object containing an array of `results` (Title, Path, Score).

### `read_article`
Reads an article from the ZIM archive using its exact Path and converts the HTML to Markdown.
- **Inputs**:
  - `path` (string, required): The exact path of the article (obtained from the `search_zim` tool).
- **Output**: A JSON object containing the `markdown` content of the article.
