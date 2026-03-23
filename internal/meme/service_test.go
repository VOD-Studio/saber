package meme

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rua.plus/saber/internal/config"
)

// createTestMemeConfig 创建测试用的 meme 配置。
func createTestMemeConfig() *config.MemeConfig {
	return &config.MemeConfig{
		Enabled:        true,
		APIKey:         "test-api-key",
		MaxResults:     5,
		TimeoutSeconds: 10,
	}
}

// createTestServer 创建测试用的 HTTP 服务器。
func createTestServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

func TestNewService(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.MemeConfig
	}{
		{
			name: "有效配置",
			cfg:  createTestMemeConfig(),
		},
		{
			name: "空配置",
			cfg:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(tt.cfg)
			if svc == nil {
				t.Fatal("NewService returned nil")
			}
			if svc.cfg != tt.cfg {
				t.Errorf("cfg not set correctly")
			}
			if svc.httpClient == nil {
				t.Error("httpClient is nil")
			}
			if svc.baseURL != "https://api.klipy.com/api/v1" {
				t.Errorf("unexpected baseURL: %s", svc.baseURL)
			}
		})
	}
}

func TestService_IsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.MemeConfig
		expected bool
	}{
		{
			name:     "已启用且有 API Key",
			cfg:      &config.MemeConfig{Enabled: true, APIKey: "test-key"},
			expected: true,
		},
		{
			name:     "已启用但无 API Key",
			cfg:      &config.MemeConfig{Enabled: true, APIKey: ""},
			expected: false,
		},
		{
			name:     "未启用",
			cfg:      &config.MemeConfig{Enabled: false, APIKey: "test-key"},
			expected: false,
		},
		{
			name:     "空配置",
			cfg:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(tt.cfg)
			if got := svc.IsEnabled(); got != tt.expected {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestService_Search(t *testing.T) {
	t.Run("成功搜索", func(t *testing.T) {
		// 创建测试服务器
		server := createTestServer(func(w http.ResponseWriter, r *http.Request) {
			// 验证请求路径
			if r.URL.Path != "/test-api-key/gifs/search" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}

			// 验证查询参数
			query := r.URL.Query().Get("q")
			if query != "happy" {
				t.Errorf("unexpected query: %s", query)
			}

			// 返回模拟响应
			resp := klipyResponse{
				Data: []klipyGIF{
					{
						ID:    "gif1",
						Title: "Happy GIF",
						Media: []klipyMedia{
							{
								GIF: klipyMediaURL{
									URL:    "https://example.com/gif1.gif",
									Width:  200,
									Height: 150,
								},
							},
						},
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		})
		defer server.Close()

		cfg := createTestMemeConfig()
		svc := NewService(cfg)
		svc.baseURL = server.URL // 替换为测试服务器 URL

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		results, err := svc.Search(ctx, "happy", ContentTypeGIF)
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}

		if results[0].ID != "gif1" {
			t.Errorf("unexpected GIF ID: %s", results[0].ID)
		}

		if results[0].URL != "https://example.com/gif1.gif" {
			t.Errorf("unexpected URL: %s", results[0].URL)
		}

		if results[0].MimeType != "image/gif" {
			t.Errorf("unexpected MimeType: %s", results[0].MimeType)
		}
	})

	t.Run("空结果", func(t *testing.T) {
		server := createTestServer(func(w http.ResponseWriter, r *http.Request) {
			resp := klipyResponse{Data: []klipyGIF{}}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		})
		defer server.Close()

		cfg := createTestMemeConfig()
		svc := NewService(cfg)
		svc.baseURL = server.URL

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		results, err := svc.Search(ctx, "nonexistent", ContentTypeGIF)
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}

		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("API 错误", func(t *testing.T) {
		server := createTestServer(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal server error"))
		})
		defer server.Close()

		cfg := createTestMemeConfig()
		svc := NewService(cfg)
		svc.baseURL = server.URL

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := svc.Search(ctx, "test", ContentTypeGIF)
		if err == nil {
			t.Error("expected error, got nil")
		}
	})

	t.Run("未配置 API Key", func(t *testing.T) {
		cfg := &config.MemeConfig{Enabled: true, APIKey: ""}
		svc := NewService(cfg)

		ctx := context.Background()
		_, err := svc.Search(ctx, "test", ContentTypeGIF)
		if err == nil {
			t.Error("expected error for missing API key")
		}
	})

	t.Run("WebP 格式", func(t *testing.T) {
		server := createTestServer(func(w http.ResponseWriter, r *http.Request) {
			resp := klipyResponse{
				Data: []klipyGIF{
					{
						ID:    "webp1",
						Title: "WebP GIF",
						Media: []klipyMedia{
							{
								GIF: klipyMediaURL{}, // 空 GIF
								Webp: klipyMediaURL{
									URL:    "https://example.com/webp1.webp",
									Width:  200,
									Height: 150,
								},
							},
						},
					},
				},
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		})
		defer server.Close()

		cfg := createTestMemeConfig()
		svc := NewService(cfg)
		svc.baseURL = server.URL

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		results, err := svc.Search(ctx, "test", ContentTypeGIF)
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}

		if results[0].MimeType != "image/webp" {
			t.Errorf("expected webp mime type, got %s", results[0].MimeType)
		}
	})
}

func TestService_GetRandom(t *testing.T) {
	t.Run("成功获取随机结果", func(t *testing.T) {
		server := createTestServer(func(w http.ResponseWriter, r *http.Request) {
			resp := klipyResponse{
				Data: []klipyGIF{
					{ID: "gif1", Title: "GIF 1", Media: []klipyMedia{{GIF: klipyMediaURL{URL: "url1"}}}},
					{ID: "gif2", Title: "GIF 2", Media: []klipyMedia{{GIF: klipyMediaURL{URL: "url2"}}}},
					{ID: "gif3", Title: "GIF 3", Media: []klipyMedia{{GIF: klipyMediaURL{URL: "url3"}}}},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		})
		defer server.Close()

		cfg := createTestMemeConfig()
		svc := NewService(cfg)
		svc.baseURL = server.URL

		ctx := context.Background()

		gif, err := svc.GetRandom(ctx, "test", ContentTypeGIF)
		if err != nil {
			t.Fatalf("GetRandom failed: %v", err)
		}

		if gif == nil {
			t.Fatal("expected non-nil GIF")
		}

		validIDs := map[string]bool{"gif1": true, "gif2": true, "gif3": true}
		if !validIDs[gif.ID] {
			t.Errorf("unexpected GIF ID: %s", gif.ID)
		}
	})

	t.Run("无结果", func(t *testing.T) {
		server := createTestServer(func(w http.ResponseWriter, r *http.Request) {
			resp := klipyResponse{Data: []klipyGIF{}}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		})
		defer server.Close()

		cfg := createTestMemeConfig()
		svc := NewService(cfg)
		svc.baseURL = server.URL

		ctx := context.Background()

		_, err := svc.GetRandom(ctx, "test", ContentTypeGIF)
		if err == nil {
			t.Error("expected error for no results")
		}
	})
}

func TestService_DownloadImage(t *testing.T) {
	t.Run("成功下载", func(t *testing.T) {
		testData := []byte("fake image data")

		server := createTestServer(func(w http.ResponseWriter, r *http.Request) {
			w.Write(testData)
		})
		defer server.Close()

		cfg := createTestMemeConfig()
		svc := NewService(cfg)

		gif := &GIF{
			ID:       "test",
			URL:      server.URL,
			MimeType: "image/gif",
		}

		ctx := context.Background()
		data, err := svc.DownloadImage(ctx, gif)
		if err != nil {
			t.Fatalf("DownloadImage failed: %v", err)
		}

		if string(data) != string(testData) {
			t.Errorf("unexpected data: got %s", string(data))
		}
	})

	t.Run("空 GIF 对象", func(t *testing.T) {
		cfg := createTestMemeConfig()
		svc := NewService(cfg)

		ctx := context.Background()
		_, err := svc.DownloadImage(ctx, nil)
		if err == nil {
			t.Error("expected error for nil GIF")
		}
	})

	t.Run("空 URL", func(t *testing.T) {
		cfg := createTestMemeConfig()
		svc := NewService(cfg)

		gif := &GIF{ID: "test", URL: ""}

		ctx := context.Background()
		_, err := svc.DownloadImage(ctx, gif)
		if err == nil {
			t.Error("expected error for empty URL")
		}
	})

	t.Run("服务器错误", func(t *testing.T) {
		server := createTestServer(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		defer server.Close()

		cfg := createTestMemeConfig()
		svc := NewService(cfg)

		gif := &GIF{ID: "test", URL: server.URL}

		ctx := context.Background()
		_, err := svc.DownloadImage(ctx, gif)
		if err == nil {
			t.Error("expected error for server error")
		}
	})
}

func TestContentType(t *testing.T) {
	tests := []struct {
		contentType ContentType
		expected    string
	}{
		{ContentTypeGIF, "gifs"},
		{ContentTypeSticker, "stickers"},
		{ContentTypeMeme, "memes"},
	}

	for _, tt := range tests {
		t.Run(string(tt.contentType), func(t *testing.T) {
			if string(tt.contentType) != tt.expected {
				t.Errorf("ContentType %s = %s, want %s", tt.contentType, string(tt.contentType), tt.expected)
			}
		})
	}
}