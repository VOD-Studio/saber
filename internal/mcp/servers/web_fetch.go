// Package servers provides built-in MCP server implementations.
package servers

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FetchInput defines the input parameters for the fetch_url tool.
type FetchInput struct {
	URL    string `json:"url" jsonschema:"required,the URL to fetch"`
	Format string `json:"format,omitempty" jsonschema:"optional,return format (text or html)"`
}

// FetchOutput defines the output of the fetch_url tool.
type FetchOutput struct {
	URL     string `json:"url" jsonschema:"the fetched URL"`
	Content string `json:"content" jsonschema:"the page content"`
	Status  string `json:"status" jsonschema:"HTTP status"`
}

// NewWebFetchServer creates a new web_fetch MCP server.
func NewWebFetchServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "web_fetch",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool[FetchInput, FetchOutput](server, &mcp.Tool{
		Name:        "fetch_url",
		Description: "获取网页内容并转换为文本",
	}, handleFetchURL)

	return server
}

func handleFetchURL(ctx context.Context, session *mcp.ServerSession, params *mcp.CallToolParamsFor[FetchInput]) (*mcp.CallToolResultFor[FetchOutput], error) {
	input := params.Arguments

	// Validate URL
	if input.URL == "" {
		return nil, fmt.Errorf("url parameter is required")
	}

	// Validate URL format (basic validation)
	if !strings.HasPrefix(input.URL, "http://") && !strings.HasPrefix(input.URL, "https://") {
		return nil, fmt.Errorf("url must start with http:// or https://")
	}

	// Create HTTP client
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}

	// Create request with context
	httpReq, err := http.NewRequestWithContext(ctx, "GET", input.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("User-Agent", "Saber-MCP-Bot/1.0")

	// Execute request
	slog.Debug("Fetching URL", "url", input.URL)
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("Failed to close response body", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error: %d %s", resp.StatusCode, resp.Status)
	}

	// Read body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Convert HTML to text if needed
	content := string(body)
	if input.Format != "html" {
		content = extractText(content)
	}

	result := &mcp.CallToolResultFor[FetchOutput]{
		StructuredContent: FetchOutput{
			URL:     input.URL,
			Content: content,
			Status:  resp.Status,
		},
		IsError: false,
	}

	return result, nil
}

// extractText strips HTML tags and returns plain text content.
func extractText(html string) string {
	// Remove script and style elements
	scriptRegex := regexp.MustCompile(`<script[^>]*>[\s\S]*?</script>`)
	styleRegex := regexp.MustCompile(`<style[^>]*>[\s\S]*?</style>`)
	html = scriptRegex.ReplaceAllString(html, "")
	html = styleRegex.ReplaceAllString(html, "")

	// Replace common HTML entities
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	html = strings.ReplaceAll(html, "&#39;", "'")

	// Strip all HTML tags
	tagRegex := regexp.MustCompile(`<[^>]+>`)
	text := tagRegex.ReplaceAllString(html, "")

	// Clean up whitespace
	text = strings.TrimSpace(text)
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")

	return text
}
