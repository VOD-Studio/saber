// Package config 提供配置管理功能。
package config

import (
	"os"
	"path/filepath"
	"strings"
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

// TestLoadRejectsOnInsecurePermissions 测试 Load 在权限不安全时拒绝加载。
func TestLoadRejectsOnInsecurePermissions(t *testing.T) {
	// 注意：此测试不能使用 t.Parallel()，因为使用了 t.Setenv

	// 创建权限过于宽松的临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "insecure-config.yaml")

	// 写入测试配置（权限 0644）
	content := ExampleConfig()
	err := os.WriteFile(configPath, []byte(content), 0o644)
	if err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	// 验证文件已创建
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatalf("config file not created")
	}

	// 验证 Load 会返回权限错误
	_, err = Load(configPath)
	if err == nil {
		t.Error("期望 Load 拒绝权限不安全的配置文件，但没有返回错误")
	}
	if !strings.Contains(err.Error(), "配置文件权限不安全") {
		t.Errorf("期望错误包含 '配置文件权限不安全'，实际错误: %v", err)
	}

	// 设置环境变量后应该可以加载
	t.Setenv("SABER_ALLOW_INSECURE_CONFIG", "true")
	_, err = Load(configPath)
	if err != nil {
		t.Errorf("设置环境变量后应该可以加载配置文件: %v", err)
	}
}
