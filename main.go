package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"

	"github.com/akhenakh/zim-cgo/zim"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// SearchResultItem defines the structured JSON format for a single search match
type SearchResultItem struct {
	Title string `json:"title"`
	Path  string `json:"path"`
	Score int    `json:"score"`
}

// SearchResponse defines the JSON envelope for the search endpoint
type SearchResponse struct {
	Results []SearchResultItem `json:"results"`
}

// ReadResponse defines the JSON envelope for the read endpoint
type ReadResponse struct {
	Markdown string `json:"markdown"`
}

// Compile regex once globally to strip HTML tags from titles
var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func main() {
	// Parse flags
	zimPath := flag.String("z", "", "Path to the .zim file")
	listenAddr := flag.String("listen", "", "Listen address for HTTP/SSE server (e.g., :4545). If empty, uses stdio.")
	flag.Parse()

	if *zimPath == "" {
		fmt.Fprintln(os.Stderr, "Error: You must provide a path to a .zim file.")
		fmt.Fprintln(os.Stderr, "Usage: mcp-zim -z <path-to-zim-file> [-listen :4545]")
		os.Exit(1)
	}

	// Open the ZIM archive
	archive, err := zim.NewArchive(*zimPath)
	if err != nil {
		log.Fatalf("Failed to open zim archive at %s: %v", *zimPath, err)
	}
	defer archive.Close()

	// Configure the HTML to Markdown Converter
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
		),
	)

	// Remove all image-related tags entirely to save LLM tokens
	conv.Register.TagType("img", converter.TagTypeRemove, converter.PriorityEarly)
	conv.Register.TagType("picture", converter.TagTypeRemove, converter.PriorityEarly)
	conv.Register.TagType("svg", converter.TagTypeRemove, converter.PriorityEarly)

	// Create a new MCP server
	s := server.NewMCPServer(
		"ZimReader",
		"1.0.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	// Tool: search_zim
	searchTool := mcp.NewTool("search_zim",
		mcp.WithDescription("Search the offline ZIM archive for articles. Returns a structured JSON array of top hits with their Title, Path, and Score."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The keyword or phrase to search for."),
		),
		mcp.WithNumber("count",
			mcp.Description("Number of results to return. Defaults to 20."),
		),
	)

	s.AddTool(searchTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !archive.HasFulltextIndex() {
			return mcp.NewToolResultError("This ZIM file does not contain a full-text search index."), nil
		}

		queryStr, err := request.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Use requested count
		count := request.GetInt("count", 20)

		searcher, err := zim.NewSearcher(archive)
		if err != nil {
			return mcp.NewToolResultError("Failed to initialize searcher: " + err.Error()), nil
		}
		defer searcher.Close()

		q, err := zim.NewQuery(queryStr)
		if err != nil {
			return mcp.NewToolResultError("Failed to parse query: " + err.Error()), nil
		}
		defer q.Close()

		search, err := searcher.Search(q)
		if err != nil {
			return mcp.NewToolResultError("Search execution failed: " + err.Error()), nil
		}
		defer search.Close()

		results, err := search.GetResults(0, count)
		if err != nil {
			return mcp.NewToolResultError("Failed to retrieve results: " + err.Error()), nil
		}

		// Prepare the JSON response container
		var respData SearchResponse

		if len(results) == 0 {
			respData.Results = []SearchResultItem{}
		} else {
			respData.Results = make([]SearchResultItem, 0, len(results))
			for _, res := range results {
				// Clean the title: Unescape HTML entities (e.g., &lt; -> <) then strip tags
				cleanTitle := html.UnescapeString(res.Title)
				cleanTitle = htmlTagRegex.ReplaceAllString(cleanTitle, "")
				cleanTitle = strings.TrimSpace(cleanTitle)

				respData.Results = append(respData.Results, SearchResultItem{
					Title: cleanTitle,
					Path:  res.Path,
					Score: res.Score,
				})
			}
		}

		// Use mcp.NewToolResultJSON to wrap the struct in a JSON envelope automatically
		res, err := mcp.NewToolResultJSON(respData)
		if err != nil {
			return mcp.NewToolResultError("Failed to serialize JSON envelope: " + err.Error()), nil
		}
		return res, nil
	})

	// Tool: read_article
	readTool := mcp.NewTool("read_article",
		mcp.WithDescription("Read an article from the ZIM archive using its exact Path. The HTML is converted to Markdown and returned within a JSON envelope."),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("The exact Path of the article (obtained from search_zim)."),
		),
	)

	s.AddTool(readTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		path, err := request.RequireString("path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		entry, err := archive.GetEntryByPath(path)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Article not found for path '%s': %v", path, err)), nil
		}
		defer entry.Close()

		// GetItem with follow redirects set to true
		item, err := entry.GetItem(true)
		if err != nil {
			return mcp.NewToolResultError("Failed to load article item: " + err.Error()), nil
		}
		defer item.Close()

		mime := item.GetMimetype()
		data := item.GetData()

		var finalMarkdown string

		// Process HTML using our customized html-to-markdown converter
		switch mime {
		case "text/html":
			md, err := conv.ConvertString(string(data))
			if err != nil {
				return mcp.NewToolResultError("Failed to convert HTML to Markdown: " + err.Error()), nil
			}

			// Post-process the Markdown to remove empty links and internal/relative navigation links
			// (?s) allows non-greedy matching across newlines if a link text happens to wrap
			linkRegex := regexp.MustCompile(`(?s)\[(.*?)\]\((.*?)\)`)

			md = linkRegex.ReplaceAllStringFunc(md, func(match string) string {
				subs := linkRegex.FindStringSubmatch(match)
				if len(subs) != 3 {
					return match
				}

				text := subs[1]
				href := subs[2]

				// If text is empty (e.g., from a removed image), drop the link entirely to avoid []()
				if strings.TrimSpace(text) == "" {
					return ""
				}

				// Extract the actual URL (ignoring markdown titles like: url "title")
				urlPart := strings.TrimSpace(strings.Split(href, " ")[0])

				// If it's a relative/internal link (no http/https), strip the markdown link format and just return the inner text
				if !strings.HasPrefix(urlPart, "http://") && !strings.HasPrefix(urlPart, "https://") {
					return text
				}

				// Otherwise, it's an external link; keep it completely intact
				return match
			})

			// Clean up any excessive newlines left behind by removed empty links
			newlineRegex := regexp.MustCompile(`\n{3,}`)
			md = newlineRegex.ReplaceAllString(md, "\n\n")

			finalMarkdown = md
		case "text/plain":
			// If it's a plain text file, return it directly
			finalMarkdown = string(data)
		default:
			// Exclude images, videos, binary blobs, etc.
			return mcp.NewToolResultError(fmt.Sprintf("Cannot read non-text article (mimetype: %s)", mime)), nil
		}

		// Put it in the JSON envelope
		res, err := mcp.NewToolResultJSON(ReadResponse{
			Markdown: finalMarkdown,
		})
		if err != nil {
			return mcp.NewToolResultError("Failed to serialize JSON envelope: " + err.Error()), nil
		}

		return res, nil
	})

	// Start the server (HTTP or Stdio)
	if *listenAddr != "" {
		log.Printf("Starting MCP server on %s (HTTP/SSE)", *listenAddr)
		httpServer := server.NewStreamableHTTPServer(s)
		if err := httpServer.Start(*listenAddr); err != nil {
			log.Fatalf("HTTP Server error: %v", err)
		}
	} else {
		// Stdio defaults to hiding logs inside standard output to prevent JSON-RPC corruption
		if err := server.ServeStdio(s); err != nil {
			log.Fatalf("Stdio Server error: %v", err)
		}
	}
}
