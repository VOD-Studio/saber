// Package matrix_test 包含 Matrix 客户端封装的单元测试。
package matrix

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/config"
)

// TestNewMatrixClient 测试 NewMatrixClient 函数。
//
// 该测试覆盖以下场景：
//   - nil 配置返回错误
//   - 无效配置返回错误
//   - 使用 token 认证创建客户端
//   - 使用密码认证创建客户端（未登录）
//   - 设置 device_id
func TestNewMatrixClient(t *testing.T) {
	// 创建测试用的 HTTP 服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 返回简单的 Matrix API 响应
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"user_id":"@test:example.com"}`)
	}))
	defer server.Close()

	tests := []struct {
		name       string
		config     *config.MatrixConfig
		wantErr    bool
		errContain string
	}{
		{
			name:       "nil 配置返回错误",
			config:     nil,
			wantErr:    true,
			errContain: "matrix config cannot be nil",
		},
		{
			name: "无效配置 - 缺少 homeserver",
			config: &config.MatrixConfig{
				UserID:      "@test:example.com",
				AccessToken: "token",
			},
			wantErr:    true,
			errContain: "invalid matrix configuration",
		},
		{
			name: "无效配置 - 缺少 user_id",
			config: &config.MatrixConfig{
				Homeserver:  server.URL,
				AccessToken: "token",
			},
			wantErr:    true,
			errContain: "invalid matrix configuration",
		},
		{
			name: "无效配置 - 缺少认证信息",
			config: &config.MatrixConfig{
				Homeserver: server.URL,
				UserID:     "@test:example.com",
			},
			wantErr:    true,
			errContain: "invalid matrix configuration",
		},
		{
			name: "使用 token 认证创建客户端",
			config: &config.MatrixConfig{
				Homeserver:  server.URL,
				UserID:      "@test:example.com",
				AccessToken: "test-token",
			},
			wantErr: false,
		},
		{
			name: "使用密码认证创建客户端",
			config: &config.MatrixConfig{
				Homeserver: server.URL,
				UserID:     "@test:example.com",
				Password:   "test-password",
			},
			wantErr: false,
		},
		{
			name: "设置 device_id",
			config: &config.MatrixConfig{
				Homeserver:  server.URL,
				UserID:      "@test:example.com",
				DeviceID:    "TESTDEVICE",
				AccessToken: "test-token",
			},
			wantErr: false,
		},
		{
			name: "token 优先于密码",
			config: &config.MatrixConfig{
				Homeserver:  server.URL,
				UserID:      "@test:example.com",
				Password:    "password",
				AccessToken: "test-token",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewMatrixClient(tt.config)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewMatrixClient() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil && tt.errContain != "" {
				if !containsString(err.Error(), tt.errContain) {
					t.Errorf("NewMatrixClient() error = %v, want error containing %q", err, tt.errContain)
				}
				return
			}

			if !tt.wantErr {
				if client == nil {
					t.Error("NewMatrixClient() returned nil client without error")
					return
				}
				// 验证配置已存储
				if client.GetConfig() != tt.config {
					t.Error("GetConfig() 返回的配置与传入的配置不一致")
				}
			}
		})
	}
}

// TestNewMatrixClient_TokenAuth 测试使用 token 认证的客户端创建。
func TestNewMatrixClient_TokenAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@bot:example.com",
		DeviceID:    "DEVICE123",
		AccessToken: "test-access-token",
	}

	client, err := NewMatrixClient(cfg)
	if err != nil {
		t.Fatalf("NewMatrixClient() error = %v", err)
	}

	// 验证 UserID
	if client.GetUserID() != id.UserID("@bot:example.com") {
		t.Errorf("GetUserID() = %v, want @bot:example.com", client.GetUserID())
	}

	// 验证 DeviceID
	if client.GetDeviceID() != id.DeviceID("DEVICE123") {
		t.Errorf("GetDeviceID() = %v, want DEVICE123", client.GetDeviceID())
	}

	// 验证已登录状态
	if !client.IsLoggedIn() {
		t.Error("IsLoggedIn() = false, want true for token auth")
	}
}

// TestNewMatrixClient_PasswordAuth 测试使用密码认证的客户端创建。
func TestNewMatrixClient_PasswordAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.MatrixConfig{
		Homeserver: server.URL,
		UserID:     "@bot:example.com",
		Password:   "test-password",
	}

	client, err := NewMatrixClient(cfg)
	if err != nil {
		t.Fatalf("NewMatrixClient() error = %v", err)
	}

	// 验证未登录状态（需要调用 Login）
	if client.IsLoggedIn() {
		t.Error("IsLoggedIn() = true, want false for password auth before login")
	}
}

// TestSaveSession 测试 SaveSession 方法。
//
// 该测试覆盖以下场景：
//   - 成功保存会话
//   - 无 access_token 时返回错误
//   - 文件权限正确设置
func TestSaveSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "session.yaml")

	tests := []struct {
		name       string
		setup      func(*testing.T) *MatrixClient
		wantErr    bool
		errContain string
	}{
		{
			name: "成功保存会话",
			setup: func(t *testing.T) *MatrixClient {
				cfg := &config.MatrixConfig{
					Homeserver:  server.URL,
					UserID:      "@bot:example.com",
					DeviceID:    "DEVICE123",
					AccessToken: "test-token",
				}
				client, err := NewMatrixClient(cfg)
				if err != nil {
					t.Fatalf("创建客户端失败: %v", err)
				}
				return client
			},
			wantErr: false,
		},
		{
			name: "无 access_token 时返回错误",
			setup: func(t *testing.T) *MatrixClient {
				cfg := &config.MatrixConfig{
					Homeserver: server.URL,
					UserID:     "@bot:example.com",
					Password:   "test-password",
				}
				client, err := NewMatrixClient(cfg)
				if err != nil {
					t.Fatalf("创建客户端失败: %v", err)
				}
				return client
			},
			wantErr:    true,
			errContain: "no access token available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.setup(t)
			err := client.SaveSession(sessionPath)

			if (err != nil) != tt.wantErr {
				t.Errorf("SaveSession() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil && tt.errContain != "" {
				if !containsString(err.Error(), tt.errContain) {
					t.Errorf("SaveSession() error = %v, want error containing %q", err, tt.errContain)
				}
				return
			}

			if !tt.wantErr {
				// 验证文件已创建
				if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
					t.Error("Session file was not created")
					return
				}

				// 验证文件权限
				info, err := os.Stat(sessionPath)
				if err != nil {
					t.Fatalf("无法获取文件信息: %v", err)
				}
				if info.Mode().Perm() != 0o600 {
					t.Errorf("文件权限 = %v, want 0600", info.Mode().Perm())
				}

				// 验证文件内容
				data, err := os.ReadFile(sessionPath)
				if err != nil {
					t.Fatalf("无法读取会话文件: %v", err)
				}

				var session Session
				if err := yaml.Unmarshal(data, &session); err != nil {
					t.Fatalf("无法解析会话文件: %v", err)
				}

				if session.UserID != "@bot:example.com" {
					t.Errorf("UserID = %v, want @bot:example.com", session.UserID)
				}
				if session.DeviceID != "DEVICE123" {
					t.Errorf("DeviceID = %v, want DEVICE123", session.DeviceID)
				}
				if session.AccessToken != "test-token" {
					t.Error("AccessToken 不匹配")
				}
			}
		})
	}
}

// TestLoadSession 测试 LoadSession 方法。
//
// 该测试覆盖以下场景：
//   - 成功加载有效会话
//   - 文件不存在返回错误
//   - 无效 YAML 返回错误
//   - 缺少 access_token 返回错误
//   - 缺少 user_id 返回错误
func TestLoadSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	tests := []struct {
		name       string
		setup      func(t *testing.T, tmpDir string) string // 返回 session 文件路径
		wantErr    bool
		errContain string
	}{
		{
			name: "成功加载有效会话",
			setup: func(t *testing.T, tmpDir string) string {
				sessionPath := filepath.Join(tmpDir, "valid_session.yaml")
				session := Session{
					UserID:      "@loaded:example.com",
					DeviceID:    "LOADEDDEVICE",
					AccessToken: "loaded-token",
					Homeserver:  "https://matrix.org",
				}
				data, err := yaml.Marshal(session)
				if err != nil {
					t.Fatalf("序列化会话失败: %v", err)
				}
				if err := os.WriteFile(sessionPath, data, 0o600); err != nil {
					t.Fatalf("写入会话文件失败: %v", err)
				}
				return sessionPath
			},
			wantErr: false,
		},
		{
			name: "文件不存在返回错误",
			setup: func(t *testing.T, tmpDir string) string {
				return filepath.Join(tmpDir, "nonexistent.yaml")
			},
			wantErr:    true,
			errContain: "session file not found",
		},
		{
			name: "无效 YAML 返回错误",
			setup: func(t *testing.T, tmpDir string) string {
				sessionPath := filepath.Join(tmpDir, "invalid_yaml.yaml")
				if err := os.WriteFile(sessionPath, []byte("invalid: [yaml"), 0o600); err != nil {
					t.Fatalf("写入测试文件失败: %v", err)
				}
				return sessionPath
			},
			wantErr:    true,
			errContain: "failed to parse session file",
		},
		{
			name: "缺少 access_token 返回错误",
			setup: func(t *testing.T, tmpDir string) string {
				sessionPath := filepath.Join(tmpDir, "no_token.yaml")
				session := Session{
					UserID:   "@test:example.com",
					DeviceID: "DEVICE",
				}
				data, _ := yaml.Marshal(session)
				if err := os.WriteFile(sessionPath, data, 0o600); err != nil {
					t.Fatalf("写入测试文件失败: %v", err)
				}
				return sessionPath
			},
			wantErr:    true,
			errContain: "no access token",
		},
		{
			name: "缺少 user_id 返回错误",
			setup: func(t *testing.T, tmpDir string) string {
				sessionPath := filepath.Join(tmpDir, "no_userid.yaml")
				session := Session{
					AccessToken: "token",
					DeviceID:    "DEVICE",
				}
				data, _ := yaml.Marshal(session)
				if err := os.WriteFile(sessionPath, data, 0o600); err != nil {
					t.Fatalf("写入测试文件失败: %v", err)
				}
				return sessionPath
			},
			wantErr:    true,
			errContain: "no user ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionPath := tt.setup(t, tmpDir)

			cfg := &config.MatrixConfig{
				Homeserver:  server.URL,
				UserID:      "@original:example.com",
				AccessToken: "original-token",
			}
			client, err := NewMatrixClient(cfg)
			if err != nil {
				t.Fatalf("创建客户端失败: %v", err)
			}

			err = client.LoadSession(sessionPath)

			if (err != nil) != tt.wantErr {
				t.Errorf("LoadSession() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil && tt.errContain != "" {
				if !containsString(err.Error(), tt.errContain) {
					t.Errorf("LoadSession() error = %v, want error containing %q", err, tt.errContain)
				}
				return
			}

			if !tt.wantErr {
				// 验证加载的值
				if client.GetUserID() != id.UserID("@loaded:example.com") {
					t.Errorf("GetUserID() = %v, want @loaded:example.com", client.GetUserID())
				}
				if client.GetDeviceID() != id.DeviceID("LOADEDDEVICE") {
					t.Errorf("GetDeviceID() = %v, want LOADEDDEVICE", client.GetDeviceID())
				}
				if !client.IsLoggedIn() {
					t.Error("IsLoggedIn() = false, want true after loading session")
				}
			}
		})
	}
}

// TestIsLoggedIn 测试 IsLoggedIn 方法。
func TestIsLoggedIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tests := []struct {
		name    string
		config  *config.MatrixConfig
		wantLog bool
	}{
		{
			name: "有 token 时已登录",
			config: &config.MatrixConfig{
				Homeserver:  server.URL,
				UserID:      "@test:example.com",
				AccessToken: "token",
			},
			wantLog: true,
		},
		{
			name: "无 token 时未登录",
			config: &config.MatrixConfig{
				Homeserver: server.URL,
				UserID:     "@test:example.com",
				Password:   "password",
			},
			wantLog: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewMatrixClient(tt.config)
			if err != nil {
				t.Fatalf("NewMatrixClient() error = %v", err)
			}

			if got := client.IsLoggedIn(); got != tt.wantLog {
				t.Errorf("IsLoggedIn() = %v, want %v", got, tt.wantLog)
			}
		})
	}
}

// TestGetClient 测试 GetClient 方法。
func TestGetClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@test:example.com",
		AccessToken: "token",
	}

	client, err := NewMatrixClient(cfg)
	if err != nil {
		t.Fatalf("NewMatrixClient() error = %v", err)
	}

	mautrixClient := client.GetClient()
	if mautrixClient == nil {
		t.Error("GetClient() returned nil")
		return
	}

	// 验证底层的 mautrix.Client
	if mautrixClient.AccessToken != "token" {
		t.Errorf("mautrix.Client.AccessToken = %v, want token", mautrixClient.AccessToken)
	}
}

// TestGetUserID 测试 GetUserID 方法。
func TestGetUserID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@mybot:matrix.org",
		AccessToken: "token",
	}

	client, err := NewMatrixClient(cfg)
	if err != nil {
		t.Fatalf("NewMatrixClient() error = %v", err)
	}

	userID := client.GetUserID()
	if userID != id.UserID("@mybot:matrix.org") {
		t.Errorf("GetUserID() = %v, want @mybot:matrix.org", userID)
	}
}

// TestGetDeviceID 测试 GetDeviceID 方法。
func TestGetDeviceID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tests := []struct {
		name         string
		deviceID     string
		wantDeviceID string
	}{
		{
			name:         "设置了 device_id",
			deviceID:     "MYDEVICE",
			wantDeviceID: "MYDEVICE",
		},
		{
			name:         "未设置 device_id",
			deviceID:     "",
			wantDeviceID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.MatrixConfig{
				Homeserver:  server.URL,
				UserID:      "@test:example.com",
				DeviceID:    tt.deviceID,
				AccessToken: "token",
			}

			client, err := NewMatrixClient(cfg)
			if err != nil {
				t.Fatalf("NewMatrixClient() error = %v", err)
			}

			deviceID := client.GetDeviceID()
			if deviceID != id.DeviceID(tt.wantDeviceID) {
				t.Errorf("GetDeviceID() = %v, want %v", deviceID, tt.wantDeviceID)
			}
		})
	}
}

// TestVerifyLogin 测试 VerifyLogin 方法。
//
// 该测试覆盖以下场景：
//   - 已设置 token 时可以调用验证
//   - 未设置 token 时返回错误
func TestVerifyLogin(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) *MatrixClient
		wantErr    bool
		errContain string
	}{
		{
			name: "未设置 token 时返回错误",
			setup: func(t *testing.T) *MatrixClient {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(server.Close)

				cfg := &config.MatrixConfig{
					Homeserver: server.URL,
					UserID:     "@test:example.com",
					Password:   "password",
				}
				client, err := NewMatrixClient(cfg)
				if err != nil {
					t.Fatalf("创建客户端失败: %v", err)
				}
				return client
			},
			wantErr:    true,
			errContain: "no access token set",
		},
		{
			name: "已设置 token 时调用 Whoami",
			setup: func(t *testing.T) *MatrixClient {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/_matrix/client/v3/account/whoami" {
						w.WriteHeader(http.StatusOK)
						_, _ = fmt.Fprint(w, `{"user_id":"@test:example.com","device_id":"DEVICE"}`)
					}
				}))
				t.Cleanup(server.Close)

				cfg := &config.MatrixConfig{
					Homeserver:  server.URL,
					UserID:      "@test:example.com",
					AccessToken: "token",
				}
				client, err := NewMatrixClient(cfg)
				if err != nil {
					t.Fatalf("创建客户端失败: %v", err)
				}
				return client
			},
			wantErr: false,
		},
		{
			name: "服务器返回错误",
			setup: func(t *testing.T) *MatrixClient {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = fmt.Fprint(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"Invalid token"}`)
				}))
				t.Cleanup(server.Close)

				cfg := &config.MatrixConfig{
					Homeserver:  server.URL,
					UserID:      "@test:example.com",
					AccessToken: "invalid-token",
				}
				client, err := NewMatrixClient(cfg)
				if err != nil {
					t.Fatalf("创建客户端失败: %v", err)
				}
				return client
			},
			wantErr:    true,
			errContain: "login verification failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.setup(t)
			ctx := context.Background()

			err := client.VerifyLogin(ctx)

			if (err != nil) != tt.wantErr {
				t.Errorf("VerifyLogin() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil && tt.errContain != "" {
				if !containsString(err.Error(), tt.errContain) {
					t.Errorf("VerifyLogin() error = %v, want error containing %q", err, tt.errContain)
				}
			}
		})
	}
}

// TestLogin 测试 Login 方法。
//
// 该测试覆盖以下场景：
//   - 未配置密码认证时返回错误
//   - 配置了密码认证但服务器错误
//   - 成功登录
func TestLogin(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) *MatrixClient
		wantErr    bool
		errContain string
	}{
		{
			name: "未配置密码认证时返回错误",
			setup: func(t *testing.T) *MatrixClient {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(server.Close)

				cfg := &config.MatrixConfig{
					Homeserver:  server.URL,
					UserID:      "@test:example.com",
					AccessToken: "token", // 有 token，不使用密码
				}
				client, err := NewMatrixClient(cfg)
				if err != nil {
					t.Fatalf("创建客户端失败: %v", err)
				}
				return client
			},
			wantErr:    true,
			errContain: "password authentication not configured",
		},
		{
			name: "服务器返回认证错误",
			setup: func(t *testing.T) *MatrixClient {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = fmt.Fprint(w, `{"errcode":"M_FORBIDDEN","error":"Invalid password"}`)
				}))
				t.Cleanup(server.Close)

				cfg := &config.MatrixConfig{
					Homeserver: server.URL,
					UserID:     "@test:example.com",
					Password:   "wrong-password",
				}
				client, err := NewMatrixClient(cfg)
				if err != nil {
					t.Fatalf("创建客户端失败: %v", err)
				}
				return client
			},
			wantErr:    true,
			errContain: "failed to login",
		},
		{
			name: "成功登录",
			setup: func(t *testing.T) *MatrixClient {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprint(w, `{
						"user_id": "@test:example.com",
						"device_id": "NEWDEVICE",
						"access_token": "new-token"
					}`)
				}))
				t.Cleanup(server.Close)

				cfg := &config.MatrixConfig{
					Homeserver: server.URL,
					UserID:     "@test:example.com",
					Password:   "correct-password",
				}
				client, err := NewMatrixClient(cfg)
				if err != nil {
					t.Fatalf("创建客户端失败: %v", err)
				}
				return client
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.setup(t)
			ctx := context.Background()

			err := client.Login(ctx)

			if (err != nil) != tt.wantErr {
				t.Errorf("Login() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil && tt.errContain != "" {
				if !containsString(err.Error(), tt.errContain) {
					t.Errorf("Login() error = %v, want error containing %q", err, tt.errContain)
				}
				return
			}

			if !tt.wantErr {
				// 验证登录后状态
				if !client.IsLoggedIn() {
					t.Error("Login() 后 IsLoggedIn() 应返回 true")
				}
				if client.GetDeviceID() != id.DeviceID("NEWDEVICE") {
					t.Errorf("GetDeviceID() = %v, want NEWDEVICE", client.GetDeviceID())
				}
			}
		})
	}
}

// TestLoadOrGeneratePickleKey 测试 LoadOrGeneratePickleKey 函数。
//
// 该测试覆盖以下场景：
//   - 文件不存在时生成新密钥
//   - 文件存在时加载密钥
//   - 文件内容长度无效时返回错误
func TestLoadOrGeneratePickleKey(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "pickle.key")

	t.Run("文件不存在时生成新密钥", func(t *testing.T) {
		key, err := LoadOrGeneratePickleKey(keyPath)
		if err != nil {
			t.Fatalf("LoadOrGeneratePickleKey() error = %v", err)
		}

		if len(key) != 32 {
			t.Errorf("密钥长度 = %d, want 32", len(key))
		}

		// 验证文件已创建
		if _, err := os.Stat(keyPath); os.IsNotExist(err) {
			t.Error("密钥文件未创建")
		}

		// 验证文件权限
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("无法获取文件信息: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("文件权限 = %v, want 0600", info.Mode().Perm())
		}
	})

	t.Run("文件存在时加载密钥", func(t *testing.T) {
		// 创建一个有效的密钥文件
		validKeyPath := filepath.Join(tmpDir, "valid.key")
		testKey := make([]byte, 32)
		for i := range 32 {
			testKey[i] = byte(i)
		}
		if err := os.WriteFile(validKeyPath, testKey, 0o600); err != nil {
			t.Fatalf("写入测试密钥文件失败: %v", err)
		}

		loadedKey, err := LoadOrGeneratePickleKey(validKeyPath)
		if err != nil {
			t.Fatalf("LoadOrGeneratePickleKey() error = %v", err)
		}

		if string(loadedKey) != string(testKey) {
			t.Error("加载的密钥与原始密钥不匹配")
		}
	})

	t.Run("文件内容长度无效时返回错误", func(t *testing.T) {
		invalidKeyPath := filepath.Join(tmpDir, "invalid.key")
		// 写入错误长度的内容
		if err := os.WriteFile(invalidKeyPath, []byte("short"), 0o600); err != nil {
			t.Fatalf("写入测试文件失败: %v", err)
		}

		_, err := LoadOrGeneratePickleKey(invalidKeyPath)
		if err == nil {
			t.Error("期望返回错误，但返回 nil")
		}
		if !containsString(err.Error(), "invalid pickle key file") {
			t.Errorf("错误消息应包含 'invalid pickle key file'，实际: %v", err)
		}
	})
}

// TestSession_Struct 测试 Session 结构体。
func TestSession_Struct(t *testing.T) {
	session := Session{
		UserID:      "@user:example.com",
		DeviceID:    "DEVICE",
		AccessToken: "token123",
		Homeserver:  "https://matrix.org",
	}

	// 测试 YAML 序列化
	data, err := yaml.Marshal(session)
	if err != nil {
		t.Fatalf("YAML 序列化失败: %v", err)
	}

	// 验证 YAML 内容包含预期字段
	yamlStr := string(data)
	// YAML 可能会给带 @ 的字符串加引号
	if !containsString(yamlStr, "@user:example.com") {
		t.Errorf("YAML 缺少 user_id 字段，内容: %s", yamlStr)
	}

	// 测试 YAML 反序列化
	var loaded Session
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("YAML 反序列化失败: %v", err)
	}

	if loaded.UserID != session.UserID {
		t.Errorf("UserID = %v, want %v", loaded.UserID, session.UserID)
	}
	if loaded.DeviceID != session.DeviceID {
		t.Errorf("DeviceID = %v, want %v", loaded.DeviceID, session.DeviceID)
	}
	if loaded.AccessToken != session.AccessToken {
		t.Errorf("AccessToken 不匹配")
	}
	if loaded.Homeserver != session.Homeserver {
		t.Errorf("Homeserver = %v, want %v", loaded.Homeserver, session.Homeserver)
	}
}

// TestGetConfig 测试 GetConfig 方法。
func TestGetConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@test:example.com",
		AccessToken: "token",
		DeviceName:  "Test Device",
	}

	client, err := NewMatrixClient(cfg)
	if err != nil {
		t.Fatalf("NewMatrixClient() error = %v", err)
	}

	returnedCfg := client.GetConfig()
	if returnedCfg != cfg {
		t.Error("GetConfig() 返回的配置不是传入的配置")
	}
	if returnedCfg.DeviceName != "Test Device" {
		t.Errorf("DeviceName = %v, want 'Test Device'", returnedCfg.DeviceName)
	}
}

// TestMatrixClient_E2EEConfig 测试 E2EE 配置处理。
func TestMatrixClient_E2EEConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	t.Run("E2EE 配置有效", func(t *testing.T) {
		cfg := &config.MatrixConfig{
			Homeserver:      server.URL,
			UserID:          "@test:example.com",
			AccessToken:     "token",
			EnableE2EE:      true,
			E2EESessionPath: filepath.Join(tmpDir, "e2ee.db"),
		}

		client, err := NewMatrixClient(cfg)
		if err != nil {
			t.Fatalf("NewMatrixClient() error = %v", err)
		}

		if client.GetConfig().EnableE2EE != true {
			t.Error("EnableE2EE 应为 true")
		}
	})

	t.Run("E2EE 缺少 session path 返回错误", func(t *testing.T) {
		cfg := &config.MatrixConfig{
			Homeserver:  server.URL,
			UserID:      "@test:example.com",
			AccessToken: "token",
			EnableE2EE:  true,
			// 缺少 E2EESessionPath
		}

		_, err := NewMatrixClient(cfg)
		if err == nil {
			t.Error("期望返回错误，但返回 nil")
		}
		if !containsString(err.Error(), "invalid matrix configuration") {
			t.Errorf("错误应包含 'invalid matrix configuration'，实际: %v", err)
		}
	})
}

// TestMatrixClient_ContextHandling 测试上下文处理。
func TestMatrixClient_ContextHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"user_id":"@test:example.com","device_id":"DEVICE"}`)
	}))
	defer server.Close()

	t.Run("取消上下文", func(t *testing.T) {
		cfg := &config.MatrixConfig{
			Homeserver:  server.URL,
			UserID:      "@test:example.com",
			AccessToken: "token",
		}

		client, err := NewMatrixClient(cfg)
		if err != nil {
			t.Fatalf("NewMatrixClient() error = %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // 立即取消

		// 取消的上下文可能导致请求失败，也可能成功（如果请求已发出）
		_ = client.VerifyLogin(ctx)
	})

	t.Run("超时上下文", func(t *testing.T) {
		cfg := &config.MatrixConfig{
			Homeserver:  server.URL,
			UserID:      "@test:example.com",
			AccessToken: "token",
		}

		client, err := NewMatrixClient(cfg)
		if err != nil {
			t.Fatalf("NewMatrixClient() error = %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()

		_ = client.VerifyLogin(ctx) // 可能因超时而失败
	})
}

// TestGetCryptoService 测试 GetCryptoService 方法。
func TestGetCryptoService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@test:example.com",
		AccessToken: "token",
	}

	client, err := NewMatrixClient(cfg)
	if err != nil {
		t.Fatalf("NewMatrixClient() error = %v", err)
	}

	// 未初始化加密服务时应返回 nil
	svc := client.GetCryptoService()
	if svc != nil {
		t.Error("GetCryptoService() 应返回 nil（未初始化）")
	}
}

// TestMatrixClient_MultipleInstances 测试创建多个客户端实例。
func TestMatrixClient_MultipleInstances(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg1 := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@bot1:example.com",
		AccessToken: "token1",
	}

	cfg2 := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@bot2:example.com",
		AccessToken: "token2",
	}

	client1, err := NewMatrixClient(cfg1)
	if err != nil {
		t.Fatalf("创建 client1 失败: %v", err)
	}

	client2, err := NewMatrixClient(cfg2)
	if err != nil {
		t.Fatalf("创建 client2 失败: %v", err)
	}

	// 验证两个客户端是独立的
	if client1.GetUserID() == client2.GetUserID() {
		t.Error("两个客户端应有不同的 UserID")
	}

	if client1.GetUserID() != id.UserID("@bot1:example.com") {
		t.Errorf("client1.UserID = %v, want @bot1:example.com", client1.GetUserID())
	}

	if client2.GetUserID() != id.UserID("@bot2:example.com") {
		t.Errorf("client2.UserID = %v, want @bot2:example.com", client2.GetUserID())
	}
}

// TestSaveSession_Overwrite 测试会话文件覆盖。
func TestSaveSession_Overwrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "session.yaml")

	// 创建第一个会话
	cfg1 := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@first:example.com",
		DeviceID:    "DEVICE1",
		AccessToken: "token1",
	}
	client1, err := NewMatrixClient(cfg1)
	if err != nil {
		t.Fatalf("创建 client1 失败: %v", err)
	}

	if err := client1.SaveSession(sessionPath); err != nil {
		t.Fatalf("保存第一个会话失败: %v", err)
	}

	// 创建第二个会话并覆盖
	cfg2 := &config.MatrixConfig{
		Homeserver:  server.URL,
		UserID:      "@second:example.com",
		DeviceID:    "DEVICE2",
		AccessToken: "token2",
	}
	client2, err := NewMatrixClient(cfg2)
	if err != nil {
		t.Fatalf("创建 client2 失败: %v", err)
	}

	if err := client2.SaveSession(sessionPath); err != nil {
		t.Fatalf("覆盖会话失败: %v", err)
	}

	// 验证会话文件内容是第二个会话
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("读取会话文件失败: %v", err)
	}

	var session Session
	if err := yaml.Unmarshal(data, &session); err != nil {
		t.Fatalf("解析会话文件失败: %v", err)
	}

	if session.UserID != "@second:example.com" {
		t.Errorf("UserID = %v, want @second:example.com", session.UserID)
	}
}

// 辅助函数：检查字符串是否包含子串
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// 确保 MatrixClient 实现了预期的方法
var _ = func() {
	// 编译时检查方法签名
	var m *MatrixClient
	_ = m.GetClient()
	_ = m.GetConfig()
	_ = m.GetUserID()
	_ = m.GetDeviceID()
	_ = m.GetCryptoService()
	_ = m.IsLoggedIn()
}

// 确保底层的 mautrix.Client 可以被正确访问
var _ = func() {
	var m *MatrixClient
	client := m.GetClient()
	_ = client.UserID
	_ = client.DeviceID
	_ = client.AccessToken
}
