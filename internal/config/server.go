package config

import (
	"fmt"
	"net"
	"path/filepath"
)

// HTTPServerConfig 控制本机聊天服务；令牌与配置分开保存，TUI 无需取得模型密钥。
type HTTPServerConfig struct {
	Listen    string `yaml:"listen"`
	TokenFile string `yaml:"token_file"`
}

// Validate 将当前版本的聊天接口限制在回环地址。
func (s HTTPServerConfig) Validate() error {
	host, _, err := net.SplitHostPort(s.Listen)
	if err != nil {
		return fmt.Errorf("server.listen 无效: %w", err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("server.listen 必须使用本机回环地址")
	}
	return nil
}

// TokenPath 将相对令牌路径固定在配置文件目录中。
func (s HTTPServerConfig) TokenPath(configPath string) string {
	name := s.TokenFile
	if name == "" {
		name = ".saber-token"
	}
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(filepath.Dir(configPath), name)
}
