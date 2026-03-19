// Package servers 测试内置 MCP 服务器功能。
package servers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rua.plus/saber/internal/config"
)

// saveWebSearchConfig 保存并恢复 webSearchConfig 的辅助函数。
func saveWebSearchConfig() func() {
	origInstances := webSearchConfig.instances
	origMaxResults := webSearchConfig.maxResults
	origTimeoutSeconds := webSearchConfig.timeoutSeconds
	return func() {
		webSearchConfig.instances = origInstances
		webSearchConfig.maxResults = origMaxResults
		webSearchConfig.timeoutSeconds = origTimeoutSeconds
	}
}

// TestConvertSearXResults_NormalResults 测试正常结果转换。
func TestConvertSearXResults_NormalResults(t *testing.T) {
	results := []struct {
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Engine  string   `json:"engine"`
		Engines []string `json:"engines"`
	}{
		{
			Title:   "Example Title",
			URL:     "https://example.com",
			Content: "This is a snippet",
			Engine:  "google",
			Engines: []string{"google"},
		},
		{
			Title:   "Another Result",
			URL:     "https://another.com",
			Content: "Another snippet",
			Engine:  "bing",
			Engines: []string{"bing"},
		},
	}

	items := convertSearXResults(results, 10)

	if len(items) != 2 {
		t.Errorf("Expected 2 items, got %d", len(items))
	}

	if items[0].Title != "Example Title" {
		t.Errorf("Expected title 'Example Title', got %q", items[0].Title)
	}
	if items[0].URL != "https://example.com" {
		t.Errorf("Expected URL 'https://example.com', got %q", items[0].URL)
	}
	if items[0].Snippet != "This is a snippet" {
		t.Errorf("Expected snippet 'This is a snippet', got %q", items[0].Snippet)
	}
	if items[0].Engine != "google" {
		t.Errorf("Expected engine 'google', got %q", items[0].Engine)
	}
}

// TestConvertSearXResults_EmptyTitle 测试空标题时使用默认值。
func TestConvertSearXResults_EmptyTitle(t *testing.T) {
	results := []struct {
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Engine  string   `json:"engine"`
		Engines []string `json:"engines"`
	}{
		{
			Title:   "",
			URL:     "https://example.com",
			Content: "Snippet",
			Engine:  "google",
		},
		{
			Title:   "   ", // 只有空格
			URL:     "https://another.com",
			Content: "Snippet",
			Engine:  "bing",
		},
	}

	items := convertSearXResults(results, 10)

	if len(items) != 2 {
		t.Errorf("Expected 2 items, got %d", len(items))
	}

	// 两个空标题都应该被替换为 "无标题"
	for i, item := range items {
		if item.Title != "无标题" {
			t.Errorf("Item %d: Expected title '无标题', got %q", i, item.Title)
		}
	}
}

// TestConvertSearXResults_EmptyURL 测试空 URL 的结果被跳过。
func TestConvertSearXResults_EmptyURL(t *testing.T) {
	results := []struct {
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Engine  string   `json:"engine"`
		Engines []string `json:"engines"`
	}{
		{
			Title:   "Valid Result",
			URL:     "https://example.com",
			Content: "Valid snippet",
			Engine:  "google",
		},
		{
			Title:   "Empty URL",
			URL:     "",
			Content: "Should be skipped",
			Engine:  "google",
		},
		{
			Title:   "Whitespace URL",
			URL:     "   ",
			Content: "Should also be skipped",
			Engine:  "bing",
		},
		{
			Title:   "Another Valid",
			URL:     "https://another.com",
			Content: "Another snippet",
			Engine:  "bing",
		},
	}

	items := convertSearXResults(results, 10)

	// 只有有效 URL 的结果应该被保留
	if len(items) != 2 {
		t.Errorf("Expected 2 items (empty URLs skipped), got %d", len(items))
	}

	if items[0].Title != "Valid Result" {
		t.Errorf("Expected first item title 'Valid Result', got %q", items[0].Title)
	}
	if items[1].Title != "Another Valid" {
		t.Errorf("Expected second item title 'Another Valid', got %q", items[1].Title)
	}
}

// TestConvertSearXResults_LongSnippet 测试长摘要截断。
func TestConvertSearXResults_LongSnippet(t *testing.T) {
	// 创建一个超过 300 字符的摘要
	longSnippet := strings.Repeat("a", 400)

	results := []struct {
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Engine  string   `json:"engine"`
		Engines []string `json:"engines"`
	}{
		{
			Title:   "Long Snippet",
			URL:     "https://example.com",
			Content: longSnippet,
			Engine:  "google",
		},
	}

	items := convertSearXResults(results, 10)

	if len(items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(items))
	}

	// 摘要应该被截断到 300 字符（297 + "..."）
	expectedLen := 300
	if len(items[0].Snippet) != expectedLen {
		t.Errorf("Expected snippet length %d, got %d", expectedLen, len(items[0].Snippet))
	}

	// 检查截断后以 "..." 结尾
	if !strings.HasSuffix(items[0].Snippet, "...") {
		t.Errorf("Expected snippet to end with '...', got %q", items[0].Snippet)
	}

	// 检查截断前的内容是正确的
	expectedPrefix := strings.Repeat("a", 297)
	if items[0].Snippet != expectedPrefix+"..." {
		t.Errorf("Snippet content mismatch")
	}
}

// TestConvertSearXResults_MaxResults 测试结果数量限制。
func TestConvertSearXResults_MaxResults(t *testing.T) {
	// 创建 10 个结果
	results := make([]struct {
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Engine  string   `json:"engine"`
		Engines []string `json:"engines"`
	}, 10)

	for i := range results {
		results[i] = struct {
			Title   string   `json:"title"`
			URL     string   `json:"url"`
			Content string   `json:"content"`
			Engine  string   `json:"engine"`
			Engines []string `json:"engines"`
		}{
			Title:   "Result",
			URL:     "https://example.com",
			Content: "Snippet",
			Engine:  "google",
		}
	}

	tests := []struct {
		name       string
		maxResults int
		want       int
	}{
		{"maxResults=3", 3, 3},
		{"maxResults=5", 5, 5},
		{"maxResults=10", 10, 10},
		{"maxResults=15 (more than available)", 15, 10},
		{"maxResults=0", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := convertSearXResults(results, tt.maxResults)
			if len(items) != tt.want {
				t.Errorf("Expected %d items, got %d", tt.want, len(items))
			}
		})
	}
}

// TestConvertSearXResults_EmptyInput 测试空输入。
func TestConvertSearXResults_EmptyInput(t *testing.T) {
	results := []struct {
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Engine  string   `json:"engine"`
		Engines []string `json:"engines"`
	}{}

	items := convertSearXResults(results, 10)

	if len(items) != 0 {
		t.Errorf("Expected 0 items for empty input, got %d", len(items))
	}
}

// TestConvertSearXResults_TrimWhitespace 测试空白字符修剪。
func TestConvertSearXResults_TrimWhitespace(t *testing.T) {
	results := []struct {
		Title   string   `json:"title"`
		URL     string   `json:"url"`
		Content string   `json:"content"`
		Engine  string   `json:"engine"`
		Engines []string `json:"engines"`
	}{
		{
			Title:   "  Title with spaces  ",
			URL:     "  https://example.com  ",
			Content: "  Snippet with spaces  ",
			Engine:  "google",
		},
	}

	items := convertSearXResults(results, 10)

	if len(items) != 1 {
		t.Fatalf("Expected 1 item, got %d", len(items))
	}

	if items[0].Title != "Title with spaces" {
		t.Errorf("Expected trimmed title, got %q", items[0].Title)
	}
	if items[0].URL != "https://example.com" {
		t.Errorf("Expected trimmed URL, got %q", items[0].URL)
	}
	if items[0].Snippet != "Snippet with spaces" {
		t.Errorf("Expected trimmed snippet, got %q", items[0].Snippet)
	}
}

// TestNewWebSearchServer 测试使用默认配置创建服务器。
func TestNewWebSearchServer(t *testing.T) {
	defer saveWebSearchConfig()()

	server := NewWebSearchServer()

	if server == nil {
		t.Error("Expected non-nil server")
	}
}

// TestNewWebSearchServerWithConfig 测试使用自定义配置创建服务器。
func TestNewWebSearchServerWithConfig(t *testing.T) {
	defer saveWebSearchConfig()()

	cfg := config.WebSearchConfig{
		Instances:      []string{"https://custom.instance.com"},
		MaxResults:     7,
		TimeoutSeconds: 30,
	}

	server := NewWebSearchServerWithConfig(cfg)

	if server == nil {
		t.Error("Expected non-nil server")
	}

	// 验证配置被正确应用
	if len(webSearchConfig.instances) != 1 || webSearchConfig.instances[0] != "https://custom.instance.com" {
		t.Errorf("Expected instances to be updated, got %v", webSearchConfig.instances)
	}
	if webSearchConfig.maxResults != 7 {
		t.Errorf("Expected maxResults to be 7, got %d", webSearchConfig.maxResults)
	}
	if webSearchConfig.timeoutSeconds != 30 {
		t.Errorf("Expected timeoutSeconds to be 30, got %d", webSearchConfig.timeoutSeconds)
	}
}

// TestNewWebSearchServerWithConfig_EmptyConfig 测试空配置使用默认值。
func TestNewWebSearchServerWithConfig_EmptyConfig(t *testing.T) {
	defer saveWebSearchConfig()()

	// 先设置自定义值
	webSearchConfig.instances = []string{"https://existing.com"}
	webSearchConfig.maxResults = 8
	webSearchConfig.timeoutSeconds = 25

	// 使用空配置
	cfg := config.WebSearchConfig{}
	server := NewWebSearchServerWithConfig(cfg)

	if server == nil {
		t.Error("Expected non-nil server")
	}

	// 配置不应被改变（空配置不会覆盖）
	if webSearchConfig.maxResults != 8 {
		t.Errorf("Expected maxResults to remain 8, got %d", webSearchConfig.maxResults)
	}
}

// TestSearchSearXNGInstance_Success 测试成功的搜索请求。
func TestSearchSearXNGInstance_Success(t *testing.T) {
	// 创建模拟 HTTP 服务器
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求参数
		if r.URL.Path != "/search" {
			t.Errorf("Expected path /search, got %s", r.URL.Path)
		}

		query := r.URL.Query().Get("q")
		if query != "test query" {
			t.Errorf("Expected query 'test query', got %q", query)
		}

		format := r.URL.Query().Get("format")
		if format != "json" {
			t.Errorf("Expected format 'json', got %q", format)
		}

		// 返回模拟响应
		resp := searxResponse{
			Query:           "test query",
			NumberOfResults: 2,
			Results: []struct {
				Title   string   `json:"title"`
				URL     string   `json:"url"`
				Content string   `json:"content"`
				Engine  string   `json:"engine"`
				Engines []string `json:"engines"`
			}{
				{
					Title:   "Result 1",
					URL:     "https://example1.com",
					Content: "Snippet 1",
					Engine:  "google",
				},
				{
					Title:   "Result 2",
					URL:     "https://example2.com",
					Content: "Snippet 2",
					Engine:  "bing",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	ctx := context.Background()
	client := &http.Client{Timeout: 5 * time.Second}

	results, err := searchSearXNGInstance(ctx, client, mockServer.URL, "test query", 10, "")

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}

	if results[0].Title != "Result 1" {
		t.Errorf("Expected first result title 'Result 1', got %q", results[0].Title)
	}
}

// TestSearchSearXNGInstance_LanguageParameter 测试语言参数传递。
func TestSearchSearXNGInstance_LanguageParameter(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		language := r.URL.Query().Get("language")
		if language != "zh" {
			t.Errorf("Expected language 'zh', got %q", language)
		}

		resp := searxResponse{}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	ctx := context.Background()
	client := &http.Client{Timeout: 5 * time.Second}

	_, err := searchSearXNGInstance(ctx, client, mockServer.URL, "test", 10, "zh")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}

// TestSearchSearXNGInstance_HTTPError 测试 HTTP 错误响应。
func TestSearchSearXNGInstance_HTTPError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	}))
	defer mockServer.Close()

	ctx := context.Background()
	client := &http.Client{Timeout: 5 * time.Second}

	results, err := searchSearXNGInstance(ctx, client, mockServer.URL, "test", 10, "")

	if err == nil {
		t.Error("Expected error for HTTP 500 response")
	}
	if results != nil {
		t.Error("Expected nil results on error")
	}

	// 验证错误消息包含状态码
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("Expected error to contain '500', got %v", err)
	}
}

// TestSearchSearXNGInstance_InvalidJSON 测试无效 JSON 响应。
func TestSearchSearXNGInstance_InvalidJSON(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("invalid json {"))
	}))
	defer mockServer.Close()

	ctx := context.Background()
	client := &http.Client{Timeout: 5 * time.Second}

	results, err := searchSearXNGInstance(ctx, client, mockServer.URL, "test", 10, "")

	if err == nil {
		t.Error("Expected error for invalid JSON response")
	}
	if results != nil {
		t.Error("Expected nil results on error")
	}
}

// TestSearchSearXNGInstance_ConnectionError 测试连接错误。
func TestSearchSearXNGInstance_ConnectionError(t *testing.T) {
	ctx := context.Background()
	client := &http.Client{Timeout: 1 * time.Second}

	// 使用无效的 URL
	results, err := searchSearXNGInstance(ctx, client, "https://invalid.host.that.does.not.exist:9999", "test", 10, "")

	if err == nil {
		t.Error("Expected error for connection failure")
	}
	if results != nil {
		t.Error("Expected nil results on connection error")
	}
}

// TestSearchSearXNGInstance_Timeout 测试请求超时。
func TestSearchSearXNGInstance_Timeout(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 故意延迟响应
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte("{}"))
	}))
	defer mockServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	client := &http.Client{Timeout: 1 * time.Second}

	results, err := searchSearXNGInstance(ctx, client, mockServer.URL, "test", 10, "")

	if err == nil {
		t.Error("Expected error for timeout")
	}
	if results != nil {
		t.Error("Expected nil results on timeout")
	}
}

// TestSearchSearXNGInstance_RequestHeaders 测试请求头设置。
func TestSearchSearXNGInstance_RequestHeaders(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent := r.Header.Get("User-Agent")
		if userAgent == "" {
			t.Error("Expected User-Agent header to be set")
		}

		accept := r.Header.Get("Accept")
		if accept != "application/json" {
			t.Errorf("Expected Accept header 'application/json', got %q", accept)
		}

		resp := searxResponse{}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	ctx := context.Background()
	client := &http.Client{Timeout: 5 * time.Second}

	_, err := searchSearXNGInstance(ctx, client, mockServer.URL, "test", 10, "")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}

// TestSearchWithSearXNG_FirstInstanceSuccess 测试第一个实例成功返回。
func TestSearchWithSearXNG_FirstInstanceSuccess(t *testing.T) {
	defer saveWebSearchConfig()()

	callCount := 0

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := searxResponse{
			Query:           "test",
			NumberOfResults: 1,
			Results: []struct {
				Title   string   `json:"title"`
				URL     string   `json:"url"`
				Content string   `json:"content"`
				Engine  string   `json:"engine"`
				Engines []string `json:"engines"`
			}{
				{Title: "Test Result", URL: "https://example.com", Content: "Test", Engine: "google"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	webSearchConfig.instances = []string{mockServer.URL}
	webSearchConfig.timeoutSeconds = 5

	ctx := context.Background()
	results, source, err := searchWithSearXNG(ctx, "test", 10, "")

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(results))
	}
	if source != mockServer.URL {
		t.Errorf("Expected source to be mock server URL, got %q", source)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}
}

// TestSearchWithSearXNG_Fallback 测试失败时降级到下一个实例。
func TestSearchWithSearXNG_Fallback(t *testing.T) {
	defer saveWebSearchConfig()()

	callCount := 0

	// 第一个实例返回错误，第二个成功
	mockServer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockServer1.Close()

	mockServer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := searxResponse{
			Query:           "test",
			NumberOfResults: 1,
			Results: []struct {
				Title   string   `json:"title"`
				URL     string   `json:"url"`
				Content string   `json:"content"`
				Engine  string   `json:"engine"`
				Engines []string `json:"engines"`
			}{
				{Title: "Test Result", URL: "https://example.com", Content: "Test", Engine: "google"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer2.Close()

	webSearchConfig.instances = []string{mockServer1.URL, mockServer2.URL}
	webSearchConfig.timeoutSeconds = 5

	ctx := context.Background()
	results, source, err := searchWithSearXNG(ctx, "test", 10, "")

	if err != nil {
		t.Errorf("Expected no error (fallback should succeed), got %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(results))
	}
	if source != mockServer2.URL {
		t.Errorf("Expected source to be second mock server URL, got %q", source)
	}
	if callCount != 2 {
		t.Errorf("Expected 2 calls (first failed, second succeeded), got %d", callCount)
	}
}

// TestSearchWithSearXNG_AllInstancesFail 测试所有实例都失败。
func TestSearchWithSearXNG_AllInstancesFail(t *testing.T) {
	defer saveWebSearchConfig()()

	mockServer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mockServer1.Close()

	mockServer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer mockServer2.Close()

	webSearchConfig.instances = []string{mockServer1.URL, mockServer2.URL}
	webSearchConfig.timeoutSeconds = 5

	ctx := context.Background()
	results, source, err := searchWithSearXNG(ctx, "test", 10, "")

	if err == nil {
		t.Error("Expected error when all instances fail")
	}
	if results != nil {
		t.Error("Expected nil results when all instances fail")
	}
	if source != "" {
		t.Errorf("Expected empty source, got %q", source)
	}

	// 验证错误消息表明所有实例都失败
	if !strings.Contains(err.Error(), "所有 SearXNG 实例都失败") {
		t.Errorf("Expected error message about all instances failing, got %v", err)
	}
}

// TestSearchWithSearXNG_EmptyInstances 测试空实例列表。
func TestSearchWithSearXNG_EmptyInstances(t *testing.T) {
	defer saveWebSearchConfig()()

	webSearchConfig.instances = []string{}
	webSearchConfig.timeoutSeconds = 5

	ctx := context.Background()
	results, source, err := searchWithSearXNG(ctx, "test", 10, "")

	if err == nil {
		t.Error("Expected error for empty instances")
	}
	if results != nil {
		t.Error("Expected nil results for empty instances")
	}
	if source != "" {
		t.Errorf("Expected empty source, got %q", source)
	}
}

// TestHandleWebSearch_EmptyQuery 测试空查询参数返回错误。
func TestHandleWebSearch_EmptyQuery(t *testing.T) {
	defer saveWebSearchConfig()()

	// 设置一个模拟服务器用于测试
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := searxResponse{}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	webSearchConfig.instances = []string{mockServer.URL}
	webSearchConfig.timeoutSeconds = 5

	ctx := context.Background()
	params := &mcp.CallToolParamsFor[SearchInput]{
		Name: "web_search",
		Arguments: SearchInput{
			Query: "",
		},
	}

	result, err := handleWebSearch(ctx, nil, params)

	if err == nil {
		t.Error("Expected error for empty query")
	}
	if result != nil {
		t.Error("Expected nil result for empty query")
	}
	if !strings.Contains(err.Error(), "query 参数不能为空") {
		t.Errorf("Expected error about empty query, got %v", err)
	}
}

// TestHandleWebSearch_NumClamping 测试 Num 参数的边界处理。
func TestHandleWebSearch_NumClamping(t *testing.T) {
	defer saveWebSearchConfig()()

	// 记录请求中的 num 参数
	var receivedMaxResults int
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SearXNG 使用 pageno 参数来控制分页，但我们在 convertSearXResults 中限制结果数量
		// 这里我们只需要返回足够多的结果来测试
		results := make([]struct {
			Title   string   `json:"title"`
			URL     string   `json:"url"`
			Content string   `json:"content"`
			Engine  string   `json:"engine"`
			Engines []string `json:"engines"`
		}, 15)

		for i := range results {
			results[i] = struct {
				Title   string   `json:"title"`
				URL     string   `json:"url"`
				Content string   `json:"content"`
				Engine  string   `json:"engine"`
				Engines []string `json:"engines"`
			}{
				Title:   "Result",
				URL:     "https://example.com",
				Content: "Snippet",
				Engine:  "google",
			}
		}

		resp := searxResponse{
			Query:           "test",
			NumberOfResults: len(results),
			Results:         results,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)

		// 从请求中读取 pageno 参数（虽然我们不直接使用它）
		receivedMaxResults++
	}))
	defer mockServer.Close()

	webSearchConfig.instances = []string{mockServer.URL}
	webSearchConfig.maxResults = 5
	webSearchConfig.timeoutSeconds = 5

	ctx := context.Background()

	tests := []struct {
		name        string
		inputNum    int
		expectedNum int
	}{
		{"默认值(0)应使用配置默认值", 0, 5},
		{"负数应使用配置默认值", -1, 5},
		{"正常值应保持不变", 3, 3},
		{"超过最大值(10)应被截断", 15, 10},
		{"最大边界值", 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := &mcp.CallToolParamsFor[SearchInput]{
				Name: "web_search",
				Arguments: SearchInput{
					Query: "test",
					Num:   tt.inputNum,
				},
			}

			result, err := handleWebSearch(ctx, nil, params)

			if err != nil {
				t.Errorf("Expected no error, got %v", err)
				return
			}

			if len(result.StructuredContent.Results) != tt.expectedNum {
				t.Errorf("Expected %d results, got %d", tt.expectedNum, len(result.StructuredContent.Results))
			}
		})
	}
}

// TestHandleWebSearch_Success 测试成功搜索返回正确结果。
func TestHandleWebSearch_Success(t *testing.T) {
	defer saveWebSearchConfig()()

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证查询参数
		query := r.URL.Query().Get("q")
		if query != "golang testing" {
			t.Errorf("Expected query 'golang testing', got %q", query)
		}

		lang := r.URL.Query().Get("language")
		if lang != "en" {
			t.Errorf("Expected language 'en', got %q", lang)
		}

		resp := searxResponse{
			Query:           "golang testing",
			NumberOfResults: 2,
			Results: []struct {
				Title   string   `json:"title"`
				URL     string   `json:"url"`
				Content string   `json:"content"`
				Engine  string   `json:"engine"`
				Engines []string `json:"engines"`
			}{
				{
					Title:   "Go Testing Tutorial",
					URL:     "https://golang.org/testing",
					Content: "Learn how to write tests in Go",
					Engine:  "google",
				},
				{
					Title:   "Table-driven Tests",
					URL:     "https://example.com/table-tests",
					Content: "Best practices for table-driven tests",
					Engine:  "bing",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	webSearchConfig.instances = []string{mockServer.URL}
	webSearchConfig.maxResults = 5
	webSearchConfig.timeoutSeconds = 5

	ctx := context.Background()
	params := &mcp.CallToolParamsFor[SearchInput]{
		Name: "web_search",
		Arguments: SearchInput{
			Query:    "golang testing",
			Num:      5,
			Language: "en",
		},
	}

	result, err := handleWebSearch(ctx, nil, params)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
		return
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.StructuredContent.Query != "golang testing" {
		t.Errorf("Expected query 'golang testing', got %q", result.StructuredContent.Query)
	}

	if len(result.StructuredContent.Results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(result.StructuredContent.Results))
	}

	if result.StructuredContent.Total != 2 {
		t.Errorf("Expected total 2, got %d", result.StructuredContent.Total)
	}

	if result.StructuredContent.Source != mockServer.URL {
		t.Errorf("Expected source to be mock server URL, got %q", result.StructuredContent.Source)
	}

	if result.IsError {
		t.Error("Expected IsError to be false")
	}
}

// TestHandleWebSearch_SearchError 测试搜索失败时返回错误。
func TestHandleWebSearch_SearchError(t *testing.T) {
	defer saveWebSearchConfig()()

	webSearchConfig.instances = []string{}
	webSearchConfig.timeoutSeconds = 5

	ctx := context.Background()
	params := &mcp.CallToolParamsFor[SearchInput]{
		Name: "web_search",
		Arguments: SearchInput{
			Query: "test",
			Num:   5,
		},
	}

	result, err := handleWebSearch(ctx, nil, params)

	if err == nil {
		t.Error("Expected error when search fails")
	}
	if result != nil {
		t.Error("Expected nil result when search fails")
	}
	if !strings.Contains(err.Error(), "搜索失败") {
		t.Errorf("Expected error about search failure, got %v", err)
	}
}
