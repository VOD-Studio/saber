// Package servers 提供内置 MCP 服务器实现。
package servers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rua.plus/saber/internal/config"
)

// SearchInput 定义搜索工具的输入参数。
type SearchInput struct {
	Query    string `json:"query" jsonschema:"required,搜索关键词"`
	Num      int    `json:"num,omitempty" jsonschema:"optional,返回结果数量(默认5,最大10)"`
	Language string `json:"language,omitempty" jsonschema:"optional,语言代码(如zh,en)"`
}

// SearchItem 定义单个搜索结果。
type SearchItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Engine  string `json:"engine,omitempty"`
}

// SearchOutput 定义搜索工具的输出。
type SearchOutput struct {
	Query   string       `json:"query"`
	Results []SearchItem `json:"results"`
	Total   int          `json:"total"`
	Source  string       `json:"source"`
}

// searxResponse 定义 SearXNG JSON 响应结构。
type searxResponse struct {
	Query           string `json:"query"`
	NumberOfResults int    `json:"number_of_results"`
	Results         []struct {
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Engine  string   `json:"engine"`
		Engines []string `json:"engines"`
	} `json:"results"`
}

// defaultSearXInstances 默认 SearXNG 实例列表（基于 searx.space 可用率统计）。
var defaultSearXInstances = []string{
	"https://seek.fyi",
	"https://search.femboy.ad",
	"https://etsi.me",
}

// webSearchConfig 存储运行时配置。
var webSearchConfig = struct {
	instances      []string
	maxResults     int
	timeoutSeconds int
}{
	instances:      defaultSearXInstances,
	maxResults:     5,
	timeoutSeconds: 20,
}

// NewWebSearchServer 创建 web_search MCP 服务器（使用默认配置）。
func NewWebSearchServer() *mcp.Server {
	return NewWebSearchServerWithConfig(config.WebSearchConfig{})
}

// NewWebSearchServerWithConfig 使用指定配置创建 web_search MCP 服务器。
func NewWebSearchServerWithConfig(cfg config.WebSearchConfig) *mcp.Server {
	// 应用配置
	if len(cfg.Instances) > 0 {
		webSearchConfig.instances = cfg.Instances
	}
	if cfg.MaxResults > 0 {
		webSearchConfig.maxResults = cfg.MaxResults
	}
	if cfg.TimeoutSeconds > 0 {
		webSearchConfig.timeoutSeconds = cfg.TimeoutSeconds
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "web_search",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "web_search",
		Description: "搜索互联网获取相关信息",
	}, handleWebSearch)

	return server
}

// handleWebSearch 处理 web_search 工具调用。
func handleWebSearch(ctx context.Context, session *mcp.ServerSession,
	params *mcp.CallToolParamsFor[SearchInput]) (*mcp.CallToolResultFor[SearchOutput], error) {

	input := params.Arguments

	if input.Query == "" {
		return nil, fmt.Errorf("query 参数不能为空")
	}

	if input.Num <= 0 {
		input.Num = webSearchConfig.maxResults
	}
	if input.Num > 10 {
		input.Num = 10
	}

	results, source, err := searchWithSearXNG(ctx, input.Query, input.Num, input.Language)
	if err != nil {
		return nil, fmt.Errorf("搜索失败: %w", err)
	}

	return &mcp.CallToolResultFor[SearchOutput]{
		StructuredContent: SearchOutput{
			Query:   input.Query,
			Results: results,
			Total:   len(results),
			Source:  source,
		},
		IsError: false,
	}, nil
}

// searchWithSearXNG 使用 SearXNG 实例进行搜索，支持多实例降级。
func searchWithSearXNG(ctx context.Context, query string, maxResults int, language string) ([]SearchItem, string, error) {
	client := &http.Client{
		Timeout: time.Duration(webSearchConfig.timeoutSeconds) * time.Second,
	}

	var lastErr error

	for _, instance := range webSearchConfig.instances {
		results, err := searchSearXNGInstance(ctx, client, instance, query, maxResults, language)
		if err != nil {
			lastErr = err
			slog.Warn("SearXNG 实例搜索失败", "instance", instance, "error", err)
			continue
		}

		slog.Info("SearXNG 搜索成功", "instance", instance, "results", len(results))
		return results, instance, nil
	}

	return nil, "", fmt.Errorf("所有 SearXNG 实例都失败: %w", lastErr)
}

// searchSearXNGInstance 向单个 SearXNG 实例发送搜索请求。
func searchSearXNGInstance(ctx context.Context, client *http.Client, instance, query string, maxResults int, language string) ([]SearchItem, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	if language != "" {
		params.Set("language", language)
	}

	searchURL := fmt.Sprintf("%s/search?%s", instance, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("User-Agent", "Saber-MCP-Bot/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("关闭响应体失败", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP 错误: %d %s", resp.StatusCode, resp.Status)
	}

	var searxResp searxResponse
	if err := json.NewDecoder(resp.Body).Decode(&searxResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return convertSearXResults(searxResp.Results, maxResults), nil
}

// convertSearXResults 将 SearXNG 结果转换为 SearchItem 列表。
func convertSearXResults(results []struct {
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	Content string   `json:"content"`
	Engine  string   `json:"engine"`
	Engines []string `json:"engines"`
}, maxResults int) []SearchItem {

	var items []SearchItem

	for i, r := range results {
		if i >= maxResults {
			break
		}

		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = "无标题"
		}

		resultURL := strings.TrimSpace(r.URL)
		if resultURL == "" {
			continue
		}

		snippet := strings.TrimSpace(r.Content)
		if len(snippet) > 300 {
			snippet = snippet[:297] + "..."
		}

		items = append(items, SearchItem{
			Title:   title,
			URL:     resultURL,
			Snippet: snippet,
			Engine:  r.Engine,
		})
	}

	return items
}
