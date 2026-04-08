// Package matrix 提供基于 mautrix-go 的 Matrix 客户端封装。
// 它处理连接、认证和会话管理。
package matrix

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/config"
)

// MatrixClient 封装了 mautrix.Client，提供会话管理能力。
// 它存储配置并提供登录、会话持久化和连接验证的方法。
type MatrixClient struct {
	client        *mautrix.Client
	config        *config.MatrixConfig
	cryptoService CryptoService // 新增：加密服务
}

// Session 表示持久化的认证状态。
// 警告：切勿将包含访问令牌的会话文件提交到版本控制。
type Session struct {
	UserID      string `yaml:"user_id"`
	DeviceID    string `yaml:"device_id"`
	AccessToken string `yaml:"access_token"`
	Homeserver  string `yaml:"homeserver"`
}

// NewMatrixClient 创建一个新的 Matrix 客户端封装。
// 它验证配置并使用现有的访问令牌初始化客户端，或准备进行基于密码的登录。
//
// 如果配置中提供了 AccessToken，它使用令牌创建客户端。
// 如果只提供 Password，客户端会被创建但需要调用 Login() 进行登录。
func NewMatrixClient(cfg *config.MatrixConfig) (*MatrixClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("matrix config cannot be nil")
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid matrix configuration: %w", err)
	}

	userID := id.UserID(cfg.UserID)
	homeserver := cfg.Homeserver

	slog.Info("Creating Matrix client",
		"homeserver", homeserver,
		"user_id", userID.String(),
		"token_auth", cfg.UseTokenAuth())

	var client *mautrix.Client
	var err error

	if cfg.UseTokenAuth() {
		client, err = mautrix.NewClient(homeserver, userID, cfg.AccessToken)
		if err != nil {
			return nil, fmt.Errorf("failed to create Matrix client with token: %w", err)
		}

		if cfg.DeviceID != "" {
			client.DeviceID = id.DeviceID(cfg.DeviceID)
		}

		slog.Info("Matrix client created with access token",
			"user_id", userID.String(),
			"device_id", cfg.DeviceID)
	} else {
		client, err = mautrix.NewClient(homeserver, userID, "")
		if err != nil {
			return nil, fmt.Errorf("failed to create Matrix client: %w", err)
		}

		if cfg.DeviceID != "" {
			client.DeviceID = id.DeviceID(cfg.DeviceID)
		}

		slog.Info("Matrix client created, ready for password login",
			"user_id", userID.String(),
			"device_id", cfg.DeviceID)
	}

	return &MatrixClient{
		client: client,
		config: cfg,
	}, nil
}

// Login 执行针对 Matrix  homeserver 的基于密码的认证。
// 此方法应仅在 UsePasswordAuth() 返回 true 时调用。
// 登录成功后，访问令牌和设备 ID 会存储在客户端中。
func (m *MatrixClient) Login(ctx context.Context) error {
	if !m.config.UsePasswordAuth() {
		return fmt.Errorf("password authentication not configured: either access token is set or password is empty")
	}

	userID := id.UserID(m.config.UserID)
	deviceName := m.config.DeviceName
	if deviceName == "" {
		deviceName = "Saber Bot"
	}

	slog.Info("Attempting Matrix password login",
		"user_id", userID.String(),
		"device_name", deviceName)

	loginReq := &mautrix.ReqLogin{
		Type: mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{
			Type: mautrix.IdentifierTypeUser,
			User: string(userID),
		},
		Password:                 m.config.Password,
		DeviceID:                 id.DeviceID(m.config.DeviceID),
		InitialDeviceDisplayName: deviceName,
	}

	resp, err := m.client.Login(ctx, loginReq)
	if err != nil {
		return fmt.Errorf("failed to login to Matrix server: %w", err)
	}

	m.client.AccessToken = resp.AccessToken
	m.client.DeviceID = resp.DeviceID

	slog.Info("Matrix login successful",
		"user_id", resp.UserID.String(),
		"device_id", resp.DeviceID.String())

	return nil
}

// SaveSession 将当前会话凭据持久化到 YAML 文件。
// 会话包括用户 ID、设备 ID、访问令牌和 homeserver。
//
// 警告：会话文件包含敏感凭据。
// 切勿将会话文件提交到版本控制。
func (m *MatrixClient) SaveSession(path string) error {
	if m.client.AccessToken == "" {
		return fmt.Errorf("cannot save session: no access token available")
	}

	session := Session{
		UserID:      m.client.UserID.String(),
		DeviceID:    m.client.DeviceID.String(),
		AccessToken: m.client.AccessToken,
		Homeserver:  m.client.HomeserverURL.String(),
	}

	data, err := yaml.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session data: %w", err)
	}

	// 以受限权限写入（0600 = 仅所有者读写）
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	slog.Info("Session saved successfully",
		"path", path,
		"user_id", session.UserID)

	return nil
}

// LoadSession 从 YAML 文件恢复会话凭据。
// 这允许重用现有的已认证会话而无需重新登录。
// 如果配置了 StrictSessionPermCheck，当会话文件权限不为 0600 时会返回错误。
func (m *MatrixClient) LoadSession(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("session file not found: %s", path)
		}
		return fmt.Errorf("failed to read session file: %w", err)
	}

	// 检查会话文件权限
	info, err := os.Stat(path)
	if err == nil {
		perm := info.Mode().Perm()
		if perm != 0o600 {
			if m.config != nil && m.config.StrictSessionPermCheck {
				return fmt.Errorf("session file has insecure permissions %04o, expected 0600", perm)
			}
			slog.Warn("Session file has insecure permissions",
				"path", path,
				"permissions", fmt.Sprintf("%04o", perm),
				"expected", "0600")
		}
	}

	var session Session
	if err := yaml.Unmarshal(data, &session); err != nil {
		return fmt.Errorf("failed to parse session file: %w", err)
	}

	if session.AccessToken == "" {
		return fmt.Errorf("session file contains no access token")
	}
	if session.UserID == "" {
		return fmt.Errorf("session file contains no user ID")
	}

	m.client.AccessToken = session.AccessToken
	m.client.DeviceID = id.DeviceID(session.DeviceID)
	m.client.UserID = id.UserID(session.UserID)

	slog.Info("Session loaded successfully",
		"path", path,
		"user_id", session.UserID,
		"device_id", session.DeviceID)

	return nil
}

// VerifyLogin 通过查询 homeserver 检查当前会话是否有效。
// 这使用 Whoami API 端点验证访问令牌是否仍然有效。
func (m *MatrixClient) VerifyLogin(ctx context.Context) error {
	if m.client.AccessToken == "" {
		return fmt.Errorf("cannot verify login: no access token set")
	}

	slog.Info("Verifying Matrix login status",
		"user_id", m.client.UserID.String())

	resp, err := m.client.Whoami(ctx)
	if err != nil {
		return fmt.Errorf("login verification failed: %w", err)
	}

	slog.Info("Matrix login verified successfully",
		"user_id", resp.UserID.String(),
		"device_id", resp.DeviceID.String())

	return nil
}

// GetClient 返回底层的 mautrix.Client 以进行高级操作。
// 使用此方法可以访问房间操作、消息发送和其他 Matrix API。
func (m *MatrixClient) GetClient() *mautrix.Client {
	return m.client
}

// GetConfig 返回当前配置。
func (m *MatrixClient) GetConfig() *config.MatrixConfig {
	return m.config
}

// GetUserID 返回当前认证用户 ID。
func (m *MatrixClient) GetUserID() id.UserID {
	return m.client.UserID
}

// GetDeviceID 返回当前设备 ID。
func (m *MatrixClient) GetDeviceID() id.DeviceID {
	return m.client.DeviceID
}

// GetCryptoService 返回加密服务实例。
func (m *MatrixClient) GetCryptoService() CryptoService {
	return m.cryptoService
}

// IsLoggedIn 如果设置了访问令牌则返回 true。
func (m *MatrixClient) IsLoggedIn() bool {
	return m.client.AccessToken != ""
}

// InitCrypto 初始化加密服务。
// 如果配置启用了 E2EE，则初始化 OlmCryptoService；否则使用 NoopCryptoService。
func (m *MatrixClient) InitCrypto(ctx context.Context, pickleKey []byte) error {
	if m.config.EnableE2EE {
		svc := NewOlmCryptoService(m.client, m.config.E2EESessionPath, pickleKey)
		if err := svc.Init(ctx); err != nil {
			return fmt.Errorf("failed to initialize crypto: %w", err)
		}
		m.cryptoService = svc
	} else {
		m.cryptoService = NewNoopCryptoService()
	}
	return nil
}

// GeneratePickleKey 生成用于加密存储的安全随机密钥。
func GeneratePickleKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate pickle key: %w", err)
	}
	return key, nil
}

// LoadOrGeneratePickleKey 从文件加载 pickle 密钥，如果文件不存在则生成并保存。
// 这确保 E2EE 加密密钥在重启后保持一致，避免 "olm account is not marked as shared" 错误。
func LoadOrGeneratePickleKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("invalid pickle key file: expected 32 bytes, got %d", len(data))
		}
		return data, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read pickle key file: %w", err)
	}

	key, err := GeneratePickleKey()
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("failed to save pickle key: %w", err)
	}

	return key, nil
}
