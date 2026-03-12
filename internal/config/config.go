package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config 存储从 YAML 配置文件加载的应用程序配置
type Config struct {
	Matrix MatrixConfig `yaml:"matrix"`
}

// MatrixConfig 存储 Matrix 连接配置
type MatrixConfig struct {
	Homeserver      string   `yaml:"homeserver"`
	UserID          string   `yaml:"user_id"`           // 完整的 Matrix ID，如 @user:matrix.org
	DeviceID        string   `yaml:"device_id"`         // 设备标识符
	DeviceName      string   `yaml:"device_name"`       // 设备显示名称
	Password        string   `yaml:"password"`          // 密码登录（可选）
	AccessToken     string   `yaml:"access_token"`      // Token 登录（可选，优先级高于密码）
	AutoJoinRooms   []string `yaml:"auto_join_rooms"`   // 启动时自动加入的房间列表
	EnableE2EE      bool     `yaml:"enable_e2ee"`       // 启用端到端加密（可选）
	E2EESessionPath string   `yaml:"e2ee_session_path"` // 端到端加密会话文件路径（可选）
	PickleKeyPath   string   `yaml:"pickle_key_path"`   // E2EE pickle 密钥文件路径（可选，默认为 e2ee_session_path + ".key"）
}

// UseTokenAuth 检查是否使用 Token 认证
func (m *MatrixConfig) UseTokenAuth() bool {
	return m.AccessToken != ""
}

// UsePasswordAuth 检查是否使用密码认证
func (m *MatrixConfig) UsePasswordAuth() bool {
	return m.Password != "" && m.AccessToken == ""
}

// Validate 验证配置是否有效
func (m *MatrixConfig) Validate() error {
	if m.Homeserver == "" {
		return fmt.Errorf("homeserver is required")
	}
	if m.UserID == "" {
		return fmt.Errorf("user_id is required")
	}
	if !m.UseTokenAuth() && !m.UsePasswordAuth() {
		return fmt.Errorf("either password or access_token must be provided")
	}
	if m.EnableE2EE && m.E2EESessionPath == "" {
		return fmt.Errorf("e2ee_session_path is required when enable_e2ee is true")
	}
	return nil
}

// DefaultConfigPath 返回默认配置文件路径
func DefaultConfigPath() string {
	return filepath.Join(".", "config.yaml")
}

// Load 读取并解析指定路径的配置文件
// 如果路径为空，则使用默认路径 (./config.yaml)
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}

// LoadOrDefault 从指定路径读取配置，如果文件不存在则返回默认配置
func LoadOrDefault(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return DefaultConfig(), nil
	}

	return Load(path)
}

// DefaultConfig 返回带有合理默认值的配置
func DefaultConfig() *Config {
	return &Config{
		Matrix: MatrixConfig{
			Homeserver:      "https://matrix.org",
			UserID:          "",
			DeviceID:        "",
			DeviceName:      "Saber Bot",
			Password:        "",
			AccessToken:     "",
			EnableE2EE:      false,
			E2EESessionPath: "",
			PickleKeyPath:   "",
		},
	}
}

// ExampleConfig returns the example configuration content.
func ExampleConfig() string {
	return `matrix:
  # Matrix 服务器地址
  homeserver: "https://matrix.org"

  # 完整的 Matrix 用户 ID（格式：@username:server.org）
  user_id: "@your-bot:matrix.org"

  # 设备标识符（可选，留空则服务器自动生成）
  device_id: "saber-bot-device"

  # 设备显示名称（可选）
  device_name: "Saber Bot"

  # 认证方式（二选一，access_token 优先级更高）
  # 方式 1: 使用 Access Token（推荐，更安全）
  access_token: "syt_xxxxxxxxxxxxx_xxxxxxxxxxxx"

  # 方式 2: 使用密码登录（首次登录使用）
  # password: "your-secure-password"

  # 启动时自动加入的房间列表（可选）
  # auto_join_rooms:
  #   - "!roomid1:matrix.org"
  #   - "#public-room:matrix.org"

  # 端到端加密（E2EE）配置（可选）
  # enable_e2ee: true  # 启用端到端加密
  # e2ee_session_path: "./saber.session"  # 加密会话文件路径
  # pickle_key_path: "./saber.session.key"  # pickle 密钥路径（可选，默认为 e2ee_session_path + ".key"）
`
}

// GenerateExample writes the example configuration to a file.
func GenerateExample(path string) error {
	return os.WriteFile(path, []byte(ExampleConfig()), 0o644)
}
