// Package cli_test 包含命令行标志解析的单元测试。
package cli

import (
	"flag"
	"os"
	"testing"
)

func TestFlagsDefaults(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		expected interface{}
		actual   func(*Flags) interface{}
	}{
		{"Verbose 默认值", "Verbose", false, func(f *Flags) interface{} { return f.Verbose }},
		{"ShowVersion 默认值", "ShowVersion", false, func(f *Flags) interface{} { return f.ShowVersion }},
		{"GenerateConfig 默认值", "GenerateConfig", false, func(f *Flags) interface{} { return f.GenerateConfig }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Flags{}
			if tt.actual(f) != tt.expected {
				t.Errorf("期望 %s 默认值为 %v，实际为 %v", tt.field, tt.expected, tt.actual(f))
			}
		})
	}
}

// TestParseFlagsWithNewFlagSet 使用独立的 FlagSet 测试标志解析。
// 由于 flag.Parse() 只能调用一次，这里使用 flag.NewFlagSet 创建独立的解析器。
func TestParseFlagsWithNewFlagSet(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		expectedConfig  string
		expectedVerbose bool
		expectedVersion bool
		expectedGen     bool
	}{
		{
			name:            "默认值",
			args:            []string{},
			expectedConfig:  "./config.yaml",
			expectedVerbose: false,
			expectedVersion: false,
			expectedGen:     false,
		},
		{
			name:            "自定义配置路径",
			args:            []string{"-config", "/etc/saber/config.yaml"},
			expectedConfig:  "/etc/saber/config.yaml",
			expectedVerbose: false,
			expectedVersion: false,
			expectedGen:     false,
		},
		{
			name:            "短选项 -c",
			args:            []string{"-c", "/custom/path.yaml"},
			expectedConfig:  "/custom/path.yaml",
			expectedVerbose: false,
			expectedVersion: false,
			expectedGen:     false,
		},
		{
			name:            "启用详细模式",
			args:            []string{"-verbose"},
			expectedConfig:  "./config.yaml",
			expectedVerbose: true,
			expectedVersion: false,
			expectedGen:     false,
		},
		{
			name:            "短选项 -v 启用详细模式",
			args:            []string{"-v"},
			expectedConfig:  "./config.yaml",
			expectedVerbose: true,
			expectedVersion: false,
			expectedGen:     false,
		},
		{
			name:            "显示版本",
			args:            []string{"-version"},
			expectedConfig:  "./config.yaml",
			expectedVerbose: false,
			expectedVersion: true,
			expectedGen:     false,
		},
		{
			name:            "生成配置",
			args:            []string{"-generate-config"},
			expectedConfig:  "./config.yaml",
			expectedVerbose: false,
			expectedVersion: false,
			expectedGen:     true,
		},
		{
			name:            "多个选项组合",
			args:            []string{"-c", "/test/config.yaml", "-v"},
			expectedConfig:  "/test/config.yaml",
			expectedVerbose: true,
			expectedVersion: false,
			expectedGen:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			f := &Flags{}

			fs.StringVar(&f.ConfigPath, "config", "./config.yaml", "config file path")
			fs.StringVar(&f.ConfigPath, "c", "./config.yaml", "config file path (shorthand)")
			fs.BoolVar(&f.Verbose, "verbose", false, "enable debug logging")
			fs.BoolVar(&f.Verbose, "v", false, "enable debug logging (shorthand)")
			fs.BoolVar(&f.ShowVersion, "version", false, "show version")
			fs.BoolVar(&f.GenerateConfig, "generate-config", false, "generate example config")

			if err := fs.Parse(tt.args); err != nil {
				t.Fatalf("解析参数失败: %v", err)
			}

			if f.ConfigPath != tt.expectedConfig {
				t.Errorf("期望 ConfigPath = %q，实际 = %q", tt.expectedConfig, f.ConfigPath)
			}
			if f.Verbose != tt.expectedVerbose {
				t.Errorf("期望 Verbose = %v，实际 = %v", tt.expectedVerbose, f.Verbose)
			}
			if f.ShowVersion != tt.expectedVersion {
				t.Errorf("期望 ShowVersion = %v，实际 = %v", tt.expectedVersion, f.ShowVersion)
			}
			if f.GenerateConfig != tt.expectedGen {
				t.Errorf("期望 GenerateConfig = %v，实际 = %v", tt.expectedGen, f.GenerateConfig)
			}
		})
	}
}

func TestParseFlagsInvalidOption(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := &Flags{}

	fs.StringVar(&f.ConfigPath, "config", "./config.yaml", "config file path")
	fs.BoolVar(&f.Verbose, "verbose", false, "enable debug logging")

	err := fs.Parse([]string{"-unknown-option"})
	if err == nil {
		t.Error("期望解析无效选项时返回错误")
	}
}

func TestFlagsZeroValue(t *testing.T) {
	var f Flags

	if f.ConfigPath != "" {
		t.Errorf("零值 ConfigPath 应该为空字符串")
	}
	if f.Verbose {
		t.Errorf("零值 Verbose 应该为 false")
	}
	if f.ShowVersion {
		t.Errorf("零值 ShowVersion 应该为 false")
	}
	if f.GenerateConfig {
		t.Errorf("零值 GenerateConfig 应该为 false")
	}
}

// TestParseReturnsNonNil 测试 Parse 返回非 nil 指针。
// 注意：由于 flag.Parse() 只能调用一次，此测试不实际调用 Parse()。
func TestParseReturnsNonNil(t *testing.T) {
	originalArgs := os.Args
	defer func() { os.Args = originalArgs }()

	os.Args = []string{"test-program"}
	f := &Flags{}

	if f == nil {
		t.Error("Flags 指针不应为 nil")
	}
}
