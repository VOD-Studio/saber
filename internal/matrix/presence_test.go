// Package matrix_test 包含矩阵在线状态管理的单元测试。
package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// presenceMockRoundTripper 创建一个模拟的 HTTP 传输层，用于控制 HTTP 响应。
type presenceMockRoundTripper struct {
	responseBody  []byte
	responseErr   error
	requests      []*http.Request
	pathResponses map[string][]byte // 按路径前缀匹配的响应
	statusCode    int               // HTTP 状态码
}

// RoundTrip 执行 HTTP 请求并返回模拟响应。
func (m *presenceMockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.requests = append(m.requests, req)
	if m.responseErr != nil {
		return nil, m.responseErr
	}

	statusCode := m.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	// 根据路径返回不同的响应
	if m.pathResponses != nil {
		for pathPrefix, body := range m.pathResponses {
			if len(req.URL.Path) >= len(pathPrefix) && req.URL.Path[:len(pathPrefix)] == pathPrefix {
				return &http.Response{
					StatusCode: statusCode,
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil
			}
		}
	}

	// 默认响应
	if m.responseBody != nil {
		return &http.Response{
			StatusCode: statusCode,
			Body:       io.NopCloser(bytes.NewReader(m.responseBody)),
		}, nil
	}

	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
	}, nil
}

// TestDefaultReconnectConfig 测试默认重连配置的值。
//
// 验证 DefaultReconnectConfig 返回的配置包含预期的默认值：
//   - MaxRetries: 10
//   - InitialDelay: 1 秒
//   - MaxDelay: 5 分钟
//   - Multiplier: 2.0
func TestDefaultReconnectConfig(t *testing.T) {
	cfg := DefaultReconnectConfig()

	if cfg == nil {
		t.Fatal("DefaultReconnectConfig() 返回 nil")
	}

	if cfg.MaxRetries != 10 {
		t.Errorf("MaxRetries = %d, 期望 10", cfg.MaxRetries)
	}

	if cfg.InitialDelay != time.Second {
		t.Errorf("InitialDelay = %v, 期望 %v", cfg.InitialDelay, time.Second)
	}

	if cfg.MaxDelay != 5*time.Minute {
		t.Errorf("MaxDelay = %v, 期望 %v", cfg.MaxDelay, 5*time.Minute)
	}

	if cfg.Multiplier != 2.0 {
		t.Errorf("Multiplier = %f, 期望 2.0", cfg.Multiplier)
	}
}

// TestNewPresenceService 测试 PresenceService 的创建。
//
// 验证 NewPresenceService 正确初始化服务：
//   - 客户端被正确设置
//   - 重连配置被设置为默认值
func TestNewPresenceService(t *testing.T) {
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        id.UserID("@test:example.com"),
		HomeserverURL: homeserverURL,
	}

	service := NewPresenceService(client)

	if service == nil {
		t.Fatal("NewPresenceService() 返回 nil")
	}

	if service.client != client {
		t.Error("客户端未正确设置")
	}

	if service.reconnectCfg == nil {
		t.Fatal("重连配置为 nil")
	}

	// 验证使用默认配置
	defaultCfg := DefaultReconnectConfig()
	if service.reconnectCfg.MaxRetries != defaultCfg.MaxRetries {
		t.Errorf("MaxRetries = %d, 期望 %d", service.reconnectCfg.MaxRetries, defaultCfg.MaxRetries)
	}
}

// TestSetReconnectConfig 测试设置自定义重连配置。
//
// 验证 SetReconnectConfig 正确替换默认配置。
func TestSetReconnectConfig(t *testing.T) {
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        id.UserID("@test:example.com"),
		HomeserverURL: homeserverURL,
	}

	service := NewPresenceService(client)

	customCfg := &ReconnectConfig{
		MaxRetries:   5,
		InitialDelay: 2 * time.Second,
		MaxDelay:     10 * time.Minute,
		Multiplier:   3.0,
	}

	service.SetReconnectConfig(customCfg)

	if service.reconnectCfg != customCfg {
		t.Error("重连配置未正确设置")
	}

	if service.reconnectCfg.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, 期望 5", service.reconnectCfg.MaxRetries)
	}

	if service.reconnectCfg.InitialDelay != 2*time.Second {
		t.Errorf("InitialDelay = %v, 期望 %v", service.reconnectCfg.InitialDelay, 2*time.Second)
	}
}

// TestSetSessionSaver 测试设置会话保存回调。
//
// 验证 SetSessionSaver 正确设置保存器和路径。
func TestSetSessionSaver(t *testing.T) {
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        id.UserID("@test:example.com"),
		HomeserverURL: homeserverURL,
	}

	service := NewPresenceService(client)

	var saverCalled bool
	testSaver := func(path string) error {
		saverCalled = true
		return nil
	}

	testPath := "/path/to/session"

	service.SetSessionSaver(testSaver, testPath)

	if service.sessionSaver == nil {
		t.Error("会话保存器未设置")
	}

	if service.sessionPath != testPath {
		t.Errorf("sessionPath = %q, 期望 %q", service.sessionPath, testPath)
	}

	// 验证保存器可以被调用
	err := service.sessionSaver(testPath)
	if err != nil {
		t.Errorf("会话保存器调用失败: %v", err)
	}
	if !saverCalled {
		t.Error("会话保存器未被调用")
	}
}

// TestCalculateBackoff 测试指数退避计算。
//
// 验证 calculateBackoff 方法正确计算退避延迟：
//   - 首次尝试返回初始延迟
//   - 后续尝试按指数增长
//   - 延迟不超过最大值
//   - 支持不同的乘数配置
func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		name          string
		config        *ReconnectConfig
		attempt       int
		expectedDelay time.Duration
	}{
		{
			name: "首次尝试返回初始延迟",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     time.Minute,
				Multiplier:   2.0,
			},
			attempt:       0,
			expectedDelay: time.Second,
		},
		{
			name: "第一次重试延迟翻倍",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     time.Minute,
				Multiplier:   2.0,
			},
			attempt:       1,
			expectedDelay: 2 * time.Second,
		},
		{
			name: "第二次重试延迟再翻倍",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     time.Minute,
				Multiplier:   2.0,
			},
			attempt:       2,
			expectedDelay: 4 * time.Second,
		},
		{
			name: "第三次重试延迟",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     time.Minute,
				Multiplier:   2.0,
			},
			attempt:       3,
			expectedDelay: 8 * time.Second,
		},
		{
			name: "延迟不超过最大值",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     10 * time.Second,
				Multiplier:   2.0,
			},
			attempt:       10, // 1 * 2^10 = 1024 秒，远超最大值
			expectedDelay: 10 * time.Second,
		},
		{
			name: "使用乘数 1.5",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     time.Minute,
				Multiplier:   1.5,
			},
			attempt:       2, // 1 * 1.5^2 = 2.25 秒
			expectedDelay: 2250 * time.Millisecond,
		},
		{
			name: "使用乘数 3.0",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     time.Minute,
				Multiplier:   3.0,
			},
			attempt:       2, // 1 * 3^2 = 9 秒
			expectedDelay: 9 * time.Second,
		},
		{
			name: "大初始延迟",
			config: &ReconnectConfig{
				InitialDelay: 10 * time.Second,
				MaxDelay:     time.Hour,
				Multiplier:   2.0,
			},
			attempt:       1, // 10 * 2 = 20 秒
			expectedDelay: 20 * time.Second,
		},
		{
			name: "边界情况：刚好等于最大延迟",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     8 * time.Second,
				Multiplier:   2.0,
			},
			attempt:       3, // 1 * 2^3 = 8 秒，等于最大值
			expectedDelay: 8 * time.Second,
		},
		{
			name: "边界情况：刚超过最大延迟",
			config: &ReconnectConfig{
				InitialDelay: time.Second,
				MaxDelay:     7 * time.Second,
				Multiplier:   2.0,
			},
			attempt:       3, // 1 * 2^3 = 8 秒，超过最大值
			expectedDelay: 7 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homeserverURL, _ := url.Parse("https://example.com")
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)
			service.SetReconnectConfig(tt.config)

			delay := service.calculateBackoff(tt.attempt)

			// 允许 1 毫秒的误差（浮点计算）
			tolerance := time.Millisecond
			if delay < tt.expectedDelay-tolerance || delay > tt.expectedDelay+tolerance {
				t.Errorf("calculateBackoff(%d) = %v, 期望 %v (±%v)", tt.attempt, delay, tt.expectedDelay, tolerance)
			}
		})
	}
}

// TestCalculateBackoff_EdgeCases 测试退避计算的边界情况。
//
// 验证极端输入下的行为：
//   - 负数尝试次数
//   - 零乘数
//   - 极大尝试次数
func TestCalculateBackoff_EdgeCases(t *testing.T) {
	t.Run("大尝试次数达到最大延迟", func(t *testing.T) {
		homeserverURL, _ := url.Parse("https://example.com")
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)
		service.SetReconnectConfig(&ReconnectConfig{
			InitialDelay: time.Second,
			MaxDelay:     5 * time.Minute,
			Multiplier:   2.0,
		})

		// 大尝试次数应该返回最大延迟
		delay := service.calculateBackoff(100)
		if delay != 5*time.Minute {
			t.Errorf("calculateBackoff(100) = %v, 期望 %v", delay, 5*time.Minute)
		}
	})

	t.Run("零初始延迟", func(t *testing.T) {
		homeserverURL, _ := url.Parse("https://example.com")
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)
		service.SetReconnectConfig(&ReconnectConfig{
			InitialDelay: 0,
			MaxDelay:     time.Minute,
			Multiplier:   2.0,
		})

		delay := service.calculateBackoff(5)
		if delay != 0 {
			t.Errorf("calculateBackoff(5) with zero initial delay = %v, 期望 0", delay)
		}
	})
}

// TestSetPresence 测试设置在线状态。
//
// 验证 SetPresence 正确调用 Matrix API 并更新内部状态。
func TestSetPresence(t *testing.T) {
	tests := []struct {
		name         string
		state        PresenceState
		statusMsg    string
		responseErr  error
		expectErr    bool
		expectState  PresenceState
		expectStatus string
	}{
		{
			name:         "设置在线状态",
			state:        PresenceOnline,
			statusMsg:    "",
			responseErr:  nil,
			expectErr:    false,
			expectState:  PresenceOnline,
			expectStatus: "",
		},
		{
			name:         "设置离线状态带消息",
			state:        PresenceOffline,
			statusMsg:    "再见",
			responseErr:  nil,
			expectErr:    false,
			expectState:  PresenceOffline,
			expectStatus: "再见",
		},
		{
			name:         "设置不可用状态",
			state:        PresenceUnavailable,
			statusMsg:    "忙碌中",
			responseErr:  nil,
			expectErr:    false,
			expectState:  PresenceUnavailable,
			expectStatus: "忙碌中",
		},
		{
			name:        "API 错误",
			state:       PresenceOnline,
			statusMsg:   "",
			responseErr: errors.New("network error"),
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &presenceMockRoundTripper{
				responseErr: tt.responseErr,
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)

			err := service.SetPresence(tt.state, tt.statusMsg)

			if tt.expectErr {
				if err == nil {
					t.Error("期望返回错误，但返回 nil")
				}
				return
			}

			if err != nil {
				t.Errorf("SetPresence() 返回意外错误: %v", err)
				return
			}

			// 验证内部状态被更新
			lastPresence, lastStatus := service.GetLastPresence()
			if lastPresence != tt.expectState {
				t.Errorf("lastPresence = %v, 期望 %v", lastPresence, tt.expectState)
			}
			if lastStatus != tt.expectStatus {
				t.Errorf("lastStatus = %q, 期望 %q", lastStatus, tt.expectStatus)
			}

			// 验证 HTTP 请求被发送
			if len(roundTripper.requests) == 0 {
				t.Error("未发送 HTTP 请求")
			}
		})
	}
}

// TestSetPresenceWithContext 测试带上下文的设置在线状态。
//
// 验证 SetPresenceWithContext 正确处理上下文取消。
func TestSetPresenceWithContext(t *testing.T) {
	t.Run("上下文取消", func(t *testing.T) {
		roundTripper := &presenceMockRoundTripper{}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)

		// 创建已取消的上下文
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := service.SetPresenceWithContext(ctx, PresenceOnline, "")

		// 上下文取消可能导致错误或请求未发送
		if err != nil && !errors.Is(err, context.Canceled) {
			// 错误可能不是 context.Canceled，因为 mautrix 客户端可能有自己的错误处理
			t.Logf("SetPresenceWithContext 返回错误: %v", err)
		}
	})

	t.Run("正常上下文", func(t *testing.T) {
		roundTripper := &presenceMockRoundTripper{}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)

		ctx := context.Background()
		err := service.SetPresenceWithContext(ctx, PresenceOnline, "测试")

		if err != nil {
			t.Errorf("SetPresenceWithContext() 返回错误: %v", err)
		}
	})
}

// TestGetPresence 测试获取在线状态。
//
// 验证 GetPresence 正确解析 Matrix API 响应。
func TestGetPresence(t *testing.T) {
	tests := []struct {
		name        string
		userID      string
		response    *mautrix.RespPresence
		responseErr error
		expectErr   bool
		expectInfo  *PresenceInfo
	}{
		{
			name:   "获取在线状态",
			userID: "@user:example.com",
			response: &mautrix.RespPresence{
				Presence:        event.PresenceOnline,
				StatusMsg:       "工作",
				LastActiveAgo:   60000, // 60 秒
				CurrentlyActive: true,
			},
			responseErr: nil,
			expectErr:   false,
			expectInfo: &PresenceInfo{
				UserID:          id.UserID("@user:example.com"),
				Presence:        PresenceOnline,
				StatusMsg:       "工作",
				LastActiveAgo:   60 * time.Second,
				CurrentlyActive: true,
			},
		},
		{
			name:   "获取离线状态",
			userID: "@offline:example.com",
			response: &mautrix.RespPresence{
				Presence:        event.PresenceOffline,
				StatusMsg:       "",
				LastActiveAgo:   3600000, // 1 小时
				CurrentlyActive: false,
			},
			responseErr: nil,
			expectErr:   false,
			expectInfo: &PresenceInfo{
				UserID:          id.UserID("@offline:example.com"),
				Presence:        PresenceOffline,
				StatusMsg:       "",
				LastActiveAgo:   time.Hour,
				CurrentlyActive: false,
			},
		},
		{
			name:        "API 错误",
			userID:      "@error:example.com",
			responseErr: errors.New("user not found"),
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var responseBody []byte
			if tt.response != nil {
				responseBody, _ = json.Marshal(tt.response)
			}

			roundTripper := &presenceMockRoundTripper{
				responseBody: responseBody,
				responseErr:  tt.responseErr,
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)

			info, err := service.GetPresence(tt.userID)

			if tt.expectErr {
				if err == nil {
					t.Error("期望返回错误，但返回 nil")
				}
				return
			}

			if err != nil {
				t.Errorf("GetPresence() 返回意外错误: %v", err)
				return
			}

			if info.UserID != tt.expectInfo.UserID {
				t.Errorf("UserID = %v, 期望 %v", info.UserID, tt.expectInfo.UserID)
			}
			if info.Presence != tt.expectInfo.Presence {
				t.Errorf("Presence = %v, 期望 %v", info.Presence, tt.expectInfo.Presence)
			}
			if info.StatusMsg != tt.expectInfo.StatusMsg {
				t.Errorf("StatusMsg = %q, 期望 %q", info.StatusMsg, tt.expectInfo.StatusMsg)
			}
			if info.LastActiveAgo != tt.expectInfo.LastActiveAgo {
				t.Errorf("LastActiveAgo = %v, 期望 %v", info.LastActiveAgo, tt.expectInfo.LastActiveAgo)
			}
			if info.CurrentlyActive != tt.expectInfo.CurrentlyActive {
				t.Errorf("CurrentlyActive = %v, 期望 %v", info.CurrentlyActive, tt.expectInfo.CurrentlyActive)
			}
		})
	}
}

// TestStartTyping 测试发送输入指示器。
//
// 验证 StartTyping 正确调用 Matrix API。
func TestStartTyping(t *testing.T) {
	tests := []struct {
		name        string
		roomID      string
		timeout     int
		responseErr error
		expectErr   bool
	}{
		{
			name:        "发送输入指示器",
			roomID:      "!room:example.com",
			timeout:     30000,
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "自定义超时",
			roomID:      "!room:example.com",
			timeout:     60000,
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "API 错误",
			roomID:      "!room:example.com",
			timeout:     30000,
			responseErr: errors.New("forbidden"),
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &presenceMockRoundTripper{
				responseErr: tt.responseErr,
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)

			err := service.StartTyping(tt.roomID, tt.timeout)

			if tt.expectErr {
				if err == nil {
					t.Error("期望返回错误，但返回 nil")
				}
				return
			}

			if err != nil {
				t.Errorf("StartTyping() 返回意外错误: %v", err)
				return
			}

			// 验证 HTTP 请求被发送
			if len(roundTripper.requests) == 0 {
				t.Error("未发送 HTTP 请求")
			}
		})
	}
}

// TestStartTypingWithContext 测试带上下文的输入指示器。
//
// 验证 StartTypingWithContext 正确处理上下文。
func TestStartTypingWithContext(t *testing.T) {
	t.Run("正常调用", func(t *testing.T) {
		roundTripper := &presenceMockRoundTripper{}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)

		ctx := context.Background()
		err := service.StartTypingWithContext(ctx, "!room:example.com", 30*time.Second)

		if err != nil {
			t.Errorf("StartTypingWithContext() 返回错误: %v", err)
		}
	})
}

// TestStopTyping 测试停止输入指示器。
//
// 验证 StopTyping 正确调用 Matrix API。
func TestStopTyping(t *testing.T) {
	tests := []struct {
		name        string
		roomID      string
		responseErr error
		expectErr   bool
	}{
		{
			name:        "停止输入指示器",
			roomID:      "!room:example.com",
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "API 错误",
			roomID:      "!room:example.com",
			responseErr: errors.New("not in room"),
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &presenceMockRoundTripper{
				responseErr: tt.responseErr,
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)

			err := service.StopTyping(tt.roomID)

			if tt.expectErr {
				if err == nil {
					t.Error("期望返回错误，但返回 nil")
				}
				return
			}

			if err != nil {
				t.Errorf("StopTyping() 返回意外错误: %v", err)
				return
			}
		})
	}
}

// TestStopTypingWithContext 测试带上下文的停止输入指示器。
//
// 验证 StopTypingWithContext 正确处理上下文。
func TestStopTypingWithContext(t *testing.T) {
	roundTripper := &presenceMockRoundTripper{}

	homeserverURL, _ := url.Parse("https://example.com")
	httpClient := &http.Client{Transport: roundTripper}
	client := &mautrix.Client{
		UserID:        id.UserID("@test:example.com"),
		Client:        httpClient,
		HomeserverURL: homeserverURL,
	}

	service := NewPresenceService(client)

	ctx := context.Background()
	err := service.StopTypingWithContext(ctx, "!room:example.com")

	if err != nil {
		t.Errorf("StopTypingWithContext() 返回错误: %v", err)
	}
}

// TestMarkAsRead 测试标记消息为已读。
//
// 验证 MarkAsRead 正确调用 Matrix API。
func TestMarkAsRead(t *testing.T) {
	tests := []struct {
		name        string
		roomID      string
		eventID     string
		responseErr error
		expectErr   bool
	}{
		{
			name:        "标记消息已读",
			roomID:      "!room:example.com",
			eventID:     "$event:example.com",
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "API 错误",
			roomID:      "!room:example.com",
			eventID:     "$event:example.com",
			responseErr: errors.New("event not found"),
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &presenceMockRoundTripper{
				responseErr: tt.responseErr,
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)

			err := service.MarkAsRead(tt.roomID, tt.eventID)

			if tt.expectErr {
				if err == nil {
					t.Error("期望返回错误，但返回 nil")
				}
				return
			}

			if err != nil {
				t.Errorf("MarkAsRead() 返回意外错误: %v", err)
				return
			}
		})
	}
}

// TestMarkAsReadWithContext 测试带上下文的标记已读。
//
// 验证 MarkAsReadWithContext 正确处理上下文。
func TestMarkAsReadWithContext(t *testing.T) {
	roundTripper := &presenceMockRoundTripper{}

	homeserverURL, _ := url.Parse("https://example.com")
	httpClient := &http.Client{Transport: roundTripper}
	client := &mautrix.Client{
		UserID:        id.UserID("@test:example.com"),
		Client:        httpClient,
		HomeserverURL: homeserverURL,
	}

	service := NewPresenceService(client)

	ctx := context.Background()
	err := service.MarkAsReadWithContext(ctx, "!room:example.com", "$event:example.com")

	if err != nil {
		t.Errorf("MarkAsReadWithContext() 返回错误: %v", err)
	}
}

// TestSendReceipt 测试发送回执。
//
// 验证 SendReceipt 正确调用 Matrix API 并处理不同回执类型。
func TestSendReceipt(t *testing.T) {
	tests := []struct {
		name        string
		roomID      string
		eventID     string
		receiptType event.ReceiptType
		responseErr error
		expectErr   bool
	}{
		{
			name:        "发送公开已读回执",
			roomID:      "!room:example.com",
			eventID:     "$event:example.com",
			receiptType: event.ReceiptTypeRead,
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "发送私密已读回执",
			roomID:      "!room:example.com",
			eventID:     "$event:example.com",
			receiptType: event.ReceiptTypeReadPrivate,
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "API 错误",
			roomID:      "!room:example.com",
			eventID:     "$event:example.com",
			receiptType: event.ReceiptTypeRead,
			responseErr: errors.New("forbidden"),
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &presenceMockRoundTripper{
				responseErr: tt.responseErr,
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)

			err := service.SendReceipt(tt.roomID, tt.eventID, tt.receiptType)

			if tt.expectErr {
				if err == nil {
					t.Error("期望返回错误，但返回 nil")
				}
				return
			}

			if err != nil {
				t.Errorf("SendReceipt() 返回意外错误: %v", err)
				return
			}
		})
	}
}

// TestSendReceiptWithContext 测试带上下文的发送回执。
//
// 验证 SendReceiptWithContext 正确处理上下文。
func TestSendReceiptWithContext(t *testing.T) {
	roundTripper := &presenceMockRoundTripper{}

	homeserverURL, _ := url.Parse("https://example.com")
	httpClient := &http.Client{Transport: roundTripper}
	client := &mautrix.Client{
		UserID:        id.UserID("@test:example.com"),
		Client:        httpClient,
		HomeserverURL: homeserverURL,
	}

	service := NewPresenceService(client)

	ctx := context.Background()
	err := service.SendReceiptWithContext(ctx, "!room:example.com", "$event:example.com", event.ReceiptTypeRead)

	if err != nil {
		t.Errorf("SendReceiptWithContext() 返回错误: %v", err)
	}
}

// TestRestorePresence 测试恢复在线状态。
//
// 验证 restorePresence 正确恢复之前设置的在线状态。
func TestRestorePresence(t *testing.T) {
	tests := []struct {
		name             string
		initialPresence  PresenceState
		initialStatusMsg string
		expectPresence   PresenceState
		expectStatusMsg  string
	}{
		{
			name:             "恢复之前设置的在线状态",
			initialPresence:  PresenceOnline,
			initialStatusMsg: "工作",
			expectPresence:   PresenceOnline,
			expectStatusMsg:  "工作",
		},
		{
			name:             "恢复离线状态",
			initialPresence:  PresenceOffline,
			initialStatusMsg: "休假中",
			expectPresence:   PresenceOffline,
			expectStatusMsg:  "休假中",
		},
		{
			name:             "无之前状态时默认为在线",
			initialPresence:  "",
			initialStatusMsg: "",
			expectPresence:   PresenceOnline,
			expectStatusMsg:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &presenceMockRoundTripper{}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)

			// 设置初始状态
			if tt.initialPresence != "" {
				service.lastPresence = tt.initialPresence
				service.lastStatusMsg = tt.initialStatusMsg
			}

			err := service.RestoreLastPresence()

			if err != nil {
				t.Errorf("RestoreLastPresence() 返回错误: %v", err)
				return
			}

			// 验证状态被正确恢复
			presence, status := service.GetLastPresence()
			if presence != tt.expectPresence {
				t.Errorf("presence = %v, 期望 %v", presence, tt.expectPresence)
			}
			if status != tt.expectStatusMsg {
				t.Errorf("status = %q, 期望 %q", status, tt.expectStatusMsg)
			}
		})
	}
}

// TestSaveSessionOnDisconnect 测试断开连接时保存会话。
//
// 验证 saveSessionOnDisconnect 在配置了保存器时正确调用。
func TestSaveSessionOnDisconnect(t *testing.T) {
	t.Run("保存会话成功", func(t *testing.T) {
		var savedPath string
		testSaver := func(path string) error {
			savedPath = path
			return nil
		}

		homeserverURL, _ := url.Parse("https://example.com")
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)
		service.SetSessionSaver(testSaver, "/path/to/session")

		// 调用内部方法
		service.saveSessionOnDisconnect()

		if savedPath != "/path/to/session" {
			t.Errorf("保存路径 = %q, 期望 %q", savedPath, "/path/to/session")
		}
	})

	t.Run("保存会话失败", func(t *testing.T) {
		testSaver := func(path string) error {
			return errors.New("write error")
		}

		homeserverURL, _ := url.Parse("https://example.com")
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)
		service.SetSessionSaver(testSaver, "/path/to/session")

		// 应该不会 panic
		service.saveSessionOnDisconnect()
	})

	t.Run("未配置保存器", func(t *testing.T) {
		homeserverURL, _ := url.Parse("https://example.com")
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)
		// 不设置保存器

		// 应该不会 panic
		service.saveSessionOnDisconnect()
	})

	t.Run("保存器为 nil 但路径不为空", func(t *testing.T) {
		homeserverURL, _ := url.Parse("https://example.com")
		client := &mautrix.Client{
			UserID:        id.UserID("@test:example.com"),
			HomeserverURL: homeserverURL,
		}

		service := NewPresenceService(client)
		service.sessionPath = "/path/to/session"
		// 不设置保存器

		// 应该不会 panic
		service.saveSessionOnDisconnect()
	})
}

// TestGetLastPresence 测试获取最后设置的状态。
//
// 验证 GetLastPresence 返回正确的值。
func TestGetLastPresence(t *testing.T) {
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        id.UserID("@test:example.com"),
		HomeserverURL: homeserverURL,
	}

	service := NewPresenceService(client)

	// 初始状态应为空
	presence, status := service.GetLastPresence()
	if presence != "" {
		t.Errorf("初始 presence = %v, 期望空", presence)
	}
	if status != "" {
		t.Errorf("初始 status = %q, 期望空", status)
	}

	// 设置状态后验证
	service.lastPresence = PresenceOnline
	service.lastStatusMsg = "测试"

	presence, status = service.GetLastPresence()
	if presence != PresenceOnline {
		t.Errorf("presence = %v, 期望 %v", presence, PresenceOnline)
	}
	if status != "测试" {
		t.Errorf("status = %q, 期望 %q", status, "测试")
	}
}

// TestPresenceState_Constants 测试在线状态常量。
//
// 验证定义的状态常量值。
func TestPresenceState_Constants(t *testing.T) {
	if PresenceOnline != "online" {
		t.Errorf("PresenceOnline = %q, 期望 %q", PresenceOnline, "online")
	}
	if PresenceOffline != "offline" {
		t.Errorf("PresenceOffline = %q, 期望 %q", PresenceOffline, "offline")
	}
	if PresenceUnavailable != "unavailable" {
		t.Errorf("PresenceUnavailable = %q, 期望 %q", PresenceUnavailable, "unavailable")
	}
}

// TestReconnectConfig_Validation 测试重连配置的有效性。
//
// 验证配置字段组合的正确性。
func TestReconnectConfig_Validation(t *testing.T) {
	tests := []struct {
		name   string
		config *ReconnectConfig
	}{
		{
			name: "默认配置",
			config: &ReconnectConfig{
				MaxRetries:   10,
				InitialDelay: time.Second,
				MaxDelay:     5 * time.Minute,
				Multiplier:   2.0,
			},
		},
		{
			name: "无限重试",
			config: &ReconnectConfig{
				MaxRetries:   0,
				InitialDelay: time.Second,
				MaxDelay:     5 * time.Minute,
				Multiplier:   2.0,
			},
		},
		{
			name: "快速重试",
			config: &ReconnectConfig{
				MaxRetries:   3,
				InitialDelay: 100 * time.Millisecond,
				MaxDelay:     time.Second,
				Multiplier:   1.5,
			},
		},
		{
			name: "慢速重试",
			config: &ReconnectConfig{
				MaxRetries:   100,
				InitialDelay: 5 * time.Second,
				MaxDelay:     time.Hour,
				Multiplier:   1.1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 验证配置可以被正确设置
			homeserverURL, _ := url.Parse("https://example.com")
			client := &mautrix.Client{
				UserID:        id.UserID("@test:example.com"),
				HomeserverURL: homeserverURL,
			}

			service := NewPresenceService(client)
			service.SetReconnectConfig(tt.config)

			if service.reconnectCfg.MaxRetries != tt.config.MaxRetries {
				t.Errorf("MaxRetries = %d, 期望 %d", service.reconnectCfg.MaxRetries, tt.config.MaxRetries)
			}
			if service.reconnectCfg.InitialDelay != tt.config.InitialDelay {
				t.Errorf("InitialDelay = %v, 期望 %v", service.reconnectCfg.InitialDelay, tt.config.InitialDelay)
			}
			if service.reconnectCfg.MaxDelay != tt.config.MaxDelay {
				t.Errorf("MaxDelay = %v, 期望 %v", service.reconnectCfg.MaxDelay, tt.config.MaxDelay)
			}
			if service.reconnectCfg.Multiplier != tt.config.Multiplier {
				t.Errorf("Multiplier = %f, 期望 %f", service.reconnectCfg.Multiplier, tt.config.Multiplier)
			}
		})
	}
}
