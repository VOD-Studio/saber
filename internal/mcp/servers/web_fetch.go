// Package servers 提供内置 MCP 服务器实现。
package servers

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxResponseBody 限制响应体大小（10MB）
const maxResponseBody = 10 * 1024 * 1024

// 预编译正则表达式（避免每次调用时重复编译）
var (
	scriptRegex     = regexp.MustCompile(`(?i)<script[^>]*>[\s\S]*?</script>`)
	styleRegex      = regexp.MustCompile(`(?i)<style[^>]*>[\s\S]*?</style>`)
	tagRegex        = regexp.MustCompile(`<[^>]+>`)
	whitespaceRegex = regexp.MustCompile(`\s+`)
	iframeRegex     = regexp.MustCompile(`(?i)<iframe[^>]*>[\s\S]*?</iframe>|<iframe[^>]*/?>`)
	objectRegex     = regexp.MustCompile(`(?i)<object[^>]*>[\s\S]*?</object>|<object[^>]*/?>`)
	embedRegex      = regexp.MustCompile(`(?i)<embed[^>]*/?>`)
	linkRegex       = regexp.MustCompile(`(?i)<link[^>]*/?>`)
	metaRegex       = regexp.MustCompile(`(?i)<meta[^>]*/?>`)
	baseRegex       = regexp.MustCompile(`(?i)<base[^>]*/?>`)
	dangerousAttrs  = regexp.MustCompile(`(?i)\s+on\w+\s*=\s*["'][^"']*["']`)
)

// FetchInput 定义 fetch_url 工具的输入参数。
type FetchInput struct {
	URL    string `json:"url" jsonschema:"required,要获取的URL"`
	Format string `json:"format,omitempty" jsonschema:"optional,返回格式(text或html)"`
}

// FetchOutput 定义 fetch_url 工具的输出。
type FetchOutput struct {
	URL     string `json:"url" jsonschema:"已获取的URL"`
	Content string `json:"content" jsonschema:"页面内容"`
	Status  string `json:"status" jsonschema:"HTTP状态"`
}

// NewWebFetchServer 创建新的 web_fetch MCP 服务器。
func NewWebFetchServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "web_fetch",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch_url",
		Description: "获取网页内容并转换为文本",
	}, handleFetchURL)

	return server
}

// cloneClientWithRedirect 创建一个带有自定义 CheckRedirect 函数的客户端克隆。
func cloneClientWithRedirect(base *http.Client, checkRedirect func(*http.Request, []*http.Request) error) *http.Client {
	// 克隆基础客户端，保留其 Transport 和其他设置
	cloned := &http.Client{
		Timeout:       base.Timeout,
		Transport:     base.Transport,
		CheckRedirect: checkRedirect,
	}
	return cloned
}

func handleFetchURL(ctx context.Context, _ *mcp.CallToolRequest, input FetchInput) (*mcp.CallToolResult, FetchOutput, error) {
	// 验证 URL
	if input.URL == "" {
		return nil, FetchOutput{}, fmt.Errorf("url 参数不能为空")
	}

	// 使用 url.Parse 进行完整验证
	u, err := url.Parse(input.URL)
	if err != nil {
		return nil, FetchOutput{}, fmt.Errorf("url 格式无效: %w", err)
	}

	// 仅允许 http/https 协议
	switch u.Scheme {
	case "http", "https":
		if u.Host == "" {
			return nil, FetchOutput{}, fmt.Errorf("url 缺少主机名")
		}
	default:
		return nil, FetchOutput{}, fmt.Errorf("仅允许 http 和 https 协议，收到: %s", u.Scheme)
	}

	// SSRF 防护：检测并阻止私有 IP 地址
	host := u.Hostname()
	if err := validateHost(host); err != nil {
		return nil, FetchOutput{}, fmt.Errorf("地址验证失败: %w", err)
	}

	// 验证并限制格式参数
	if input.Format != "" && input.Format != "text" && input.Format != "html" {
		return nil, FetchOutput{}, fmt.Errorf("format 必须是 'text' 或 'html'，收到: %s", input.Format)
	}
	if input.Format == "" {
		input.Format = "text"
	}

	// 获取共享客户端并克隆以添加自定义 CheckRedirect
	baseClient := GetSharedHTTPClient()
	client := cloneClientWithRedirect(baseClient, func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("重定向超过 10 次")
		}
		// 验证重定向目标
		if err := validateHost(req.URL.Hostname()); err != nil {
			return fmt.Errorf("重定向目标无效: %w", err)
		}
		return nil
	})

	// 创建带上下文的请求
	httpReq, err := http.NewRequestWithContext(ctx, "GET", input.URL, nil)
	if err != nil {
		return nil, FetchOutput{}, fmt.Errorf("创建请求失败: %w", err)
	}

	httpReq.Header.Set("User-Agent", "Saber-MCP-Bot/1.0")

	// 执行请求
	slog.Debug("正在获取 URL", "url", input.URL)
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, FetchOutput{}, fmt.Errorf("请求失败: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("关闭响应体失败", "error", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, FetchOutput{}, fmt.Errorf("HTTP 错误: %d %s", resp.StatusCode, resp.Status)
	}

	// 限制响应体大小
	limitedReader := io.LimitReader(resp.Body, maxResponseBody)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, FetchOutput{}, fmt.Errorf("读取响应失败: %w", err)
	}

	// 转换 HTML 为文本
	content := string(body)
	if input.Format != "html" {
		content = extractText(content)
	}

	return nil, FetchOutput{
		URL:     input.URL,
		Content: content,
		Status:  resp.Status,
	}, nil
}

// validateHost 验证主机名，阻止对私有 IP 地址的访问（SSRF 防护）。
func validateHost(host string) error {
	// 解析主机名获取 IP 地址
	ips, err := net.LookupIP(host)
	if err != nil {
		// DNS 解析失败可能是域名不存在，让 HTTP 请求自行处理
		return nil
	}

	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("禁止访问私有 IP 地址: %s", ip.String())
		}
	}

	return nil
}

// isPrivateIP 检测 IP 是否为私有地址或特殊地址。
func isPrivateIP(ip net.IP) bool {
	// 回环地址 127.0.0.0/8
	if ip.IsLoopback() {
		return true
	}

	// 私有地址范围
	// 10.0.0.0/8
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		// 169.254.0.0/16 (链路本地)
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		// 0.0.0.0/8 (当前网络)
		if ip4[0] == 0 {
			return true
		}
	}

	// IPv6 私有地址
	if ip.IsPrivate() {
		return true
	}

	// 链路本地地址
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	return false
}

// extractText 去除 HTML 标签并返回纯文本内容。
func extractText(html string) string {
	// 移除危险元素 (iframe, object, embed, link, meta, base)
	html = iframeRegex.ReplaceAllString(html, "")
	html = objectRegex.ReplaceAllString(html, "")
	html = embedRegex.ReplaceAllString(html, "")
	html = linkRegex.ReplaceAllString(html, "")
	html = metaRegex.ReplaceAllString(html, "")
	html = baseRegex.ReplaceAllString(html, "")

	// 移除 script 和 style 元素
	html = scriptRegex.ReplaceAllString(html, "")
	html = styleRegex.ReplaceAllString(html, "")

	// 移除危险属性 (onclick, onerror, onload 等)
	html = dangerousAttrs.ReplaceAllString(html, "")

	// 替换常见 HTML 实体
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	html = strings.ReplaceAll(html, "&#39;", "'")

	// 去除所有 HTML 标签
	text := tagRegex.ReplaceAllString(html, "")

	// 清理空白字符
	text = strings.TrimSpace(text)
	text = whitespaceRegex.ReplaceAllString(text, " ")

	return text
}
