package config

import "errors"

// VaultConfig 决定秘密的本机存储及明确启用的密码管理器；禁止模型读取秘密。
type VaultConfig struct {
	Enabled            bool                  `yaml:"enabled"`
	Directory          string                `yaml:"directory"`
	KeyStorage         string                `yaml:"key_storage"`
	OnePassword        PasswordManagerConfig `yaml:"onepassword"`
	Bitwarden          PasswordManagerConfig `yaml:"bitwarden"`
	IdleTimeoutSeconds int                   `yaml:"idle_timeout_seconds"`
}

// PasswordManagerConfig 只允许受信配置选择 CLI 和宿主环境凭据。
type PasswordManagerConfig struct {
	Enabled      bool     `yaml:"enabled"`
	Command      string   `yaml:"command"`
	Account      string   `yaml:"account"`
	TokenEnv     string   `yaml:"token_env"`
	AllowedUsers []string `yaml:"allowed_users"`
}

// DefaultVaultConfig 返回本机 AES-GCM vault 的保守默认值。
func DefaultVaultConfig() VaultConfig {
	return VaultConfig{Enabled: true, KeyStorage: "file", IdleTimeoutSeconds: 1800, OnePassword: PasswordManagerConfig{Command: "op", TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"}, Bitwarden: PasswordManagerConfig{Command: "bw"}}
}

// Validate 拒绝不支持的密钥后端和无限管理器会话。
func (c VaultConfig) Validate() error {
	if c.KeyStorage != "file" && c.KeyStorage != "keychain" {
		return errors.New("vault.key_storage must be file or keychain")
	}
	if c.IdleTimeoutSeconds < 1 || c.IdleTimeoutSeconds > 86400 {
		return errors.New("vault.idle_timeout_seconds must be 1..86400")
	}
	return nil
}
