// Package meme 提供 Klipy API 集成，用于搜索 GIF/Sticker/Meme。
package meme

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"time"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/mcp/servers"
)

// ContentType 定义 Klipy API 支持的内容类型。
type ContentType string

const (
	// ContentTypeGIF 表示 GIF 动图类型。
	ContentTypeGIF ContentType = "gifs"
	// ContentTypeSticker 表示贴纸类型。
	ContentTypeSticker ContentType = "stickers"
	// ContentTypeMeme 表示静态梗图类型。
	ContentTypeMeme ContentType = "memes"
)

// GIF 表示一个 GIF/Meme 结果。
type GIF struct {
	ID    string
	Title string
	URL   string
	Width int
	// Height 图片高度
	Height int
	// MimeType MIME 类型
	MimeType string
}

// Service 提供 Klipy API 访问。
type Service struct {
	cfg        *config.MemeConfig
	httpClient *http.Client
	baseURL    string
}

// klipyResponse 定义 Klipy API 响应结构。
type klipyResponse struct {
	Data []klipyGIF `json:"data"`
}

// klipyGIF 定义 Klipy API 返回的 GIF 结构。
type klipyGIF struct {
	ID    string       `json:"id"`
	Title string       `json:"title"`
	Media []klipyMedia `json:"media"`
}

// klipyMedia 定义 Klipy API 返回的媒体结构。
type klipyMedia struct {
	GIF  klipyMediaURL `json:"gif"`
	Webp klipyMediaURL `json:"webp"`
}

// klipyMediaURL 定义 Klipy API 返回的媒体 URL 结构。
type klipyMediaURL struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// NewService 创建一个新的 meme 服务实例。
func NewService(cfg *config.MemeConfig) *Service {
	return &Service{
		cfg:        cfg,
		httpClient: servers.GetSharedHTTPClient(),
		baseURL:    "https://api.klipy.com/api/v1",
	}
}

// Search 搜索指定类型的内容。
//
// 参数:
//   - ctx: 上下文
//   - query: 搜索关键词
//   - contentType: 内容类型（gifs, stickers, memes）
//
// 返回值:
//   - []*GIF: 搜索结果列表
//   - error: 错误信息
func (s *Service) Search(ctx context.Context, query string, contentType ContentType) ([]*GIF, error) {
	if s.cfg == nil || s.cfg.APIKey == "" {
		return nil, fmt.Errorf("meme service not configured")
	}

	// 构建请求 URL
	// 格式: https://api.klipy.com/api/v1/{API_KEY}/{type}/search?q={query}
	endpoint := fmt.Sprintf("%s/%s/%s/search", s.baseURL, s.cfg.APIKey, contentType)

	params := url.Values{}
	params.Set("q", query)
	if s.cfg.MaxResults > 0 {
		params.Set("limit", fmt.Sprintf("%d", s.cfg.MaxResults))
	}

	fullURL := endpoint + "?" + params.Encode()

	slog.Debug("Klipy API request", "url", fullURL, "type", contentType, "query", query)

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	// 发送请求
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: status %d, body: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var klipyResp klipyResponse
	if err := json.NewDecoder(resp.Body).Decode(&klipyResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// 转换为 GIF 结构
	results := make([]*GIF, 0, len(klipyResp.Data))
	for _, item := range klipyResp.Data {
		gif := s.parseKlipyGIF(item)
		if gif != nil {
			results = append(results, gif)
		}
	}

	slog.Debug("Klipy API response", "count", len(results), "query", query)

	return results, nil
}

// GetRandom 随机获取一个结果。
//
// 参数:
//   - ctx: 上下文
//   - query: 搜索关键词
//   - contentType: 内容类型
//
// 返回值:
//   - *GIF: 随机选择的 GIF
//   - error: 错误信息
func (s *Service) GetRandom(ctx context.Context, query string, contentType ContentType) (*GIF, error) {
	results, err := s.Search(ctx, query, contentType)
	if err != nil {
		return nil, err
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no results found for query: %s", query)
	}

	// 随机选择一个结果
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	idx := r.Intn(len(results))

	return results[idx], nil
}

// DownloadImage 下载图片内容。
//
// 参数:
//   - ctx: 上下文
//   - gif: GIF 对象
//
// 返回值:
//   - []byte: 图片数据
//   - error: 错误信息
func (s *Service) DownloadImage(ctx context.Context, gif *GIF) ([]byte, error) {
	if gif == nil || gif.URL == "" {
		return nil, fmt.Errorf("invalid GIF object")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gif.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create download request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read image data: %w", err)
	}

	return data, nil
}

// parseKlipyGIF 将 Klipy API 响应转换为 GIF 结构。
func (s *Service) parseKlipyGIF(item klipyGIF) *GIF {
	if len(item.Media) == 0 {
		return nil
	}

	media := item.Media[0]

	// 优先使用 GIF 格式，其次 WebP
	var mediaURL klipyMediaURL
	var mimeType string

	if media.GIF.URL != "" {
		mediaURL = media.GIF
		mimeType = "image/gif"
	} else if media.Webp.URL != "" {
		mediaURL = media.Webp
		mimeType = "image/webp"
	} else {
		return nil
	}

	return &GIF{
		ID:       item.ID,
		Title:    item.Title,
		URL:      mediaURL.URL,
		Width:    mediaURL.Width,
		Height:   mediaURL.Height,
		MimeType: mimeType,
	}
}

// IsEnabled 检查服务是否已启用。
func (s *Service) IsEnabled() bool {
	return s.cfg != nil && s.cfg.Enabled && s.cfg.APIKey != ""
}
