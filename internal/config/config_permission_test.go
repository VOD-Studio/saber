// Package config 提供配置管理功能。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateExamplePermissions 测试 GenerateExample 创建的文件权限。
func TestGenerateExamplePermissions(t *testing.T) {
	t.Parallel()

	// 创建临时目录
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-config.yaml")

	// 调用 GenerateExample
	err := GenerateExample(configPath)
	if err != nil {
		t.Fatalf("GenerateExample() error = %v", err)
	}

	// 检查文件权限
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}

	// 验证权限为 0600
	expectedPerm := os.FileMode(0o600)
	if info.Mode().Perm() != expectedPerm {
		t.Errorf("GenerateExample() file permission = %o, want %o", info.Mode().Perm(), expectedPerm)
	}
}
