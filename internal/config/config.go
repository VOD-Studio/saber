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
	Homeserver string `yaml:"homeserver"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
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
			Homeserver: "https://matrix.org",
			Username:   "",
			Password:   "",
		},
	}
}
