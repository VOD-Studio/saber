// Package platform_test 验证平台注册表的行为契约。
package platform

import (
	"context"
	"testing"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// stubPlatform 是测试用的平台桩，只暴露固定名称。
type stubPlatform struct {
	name string
}

func (s *stubPlatform) Name() string                                    { return s.name }
func (s *stubPlatform) Start(ctx context.Context, h chat.Handler) error { return nil }
func (s *stubPlatform) Stop()                                           {}

// newTestRegistry 用给定平台构造注册表。
func newTestRegistry(ps ...Platform) *Registry {
	r := NewRegistry()
	for _, p := range ps {
		r.Register(p)
	}
	return r
}

// TestRegistry_Register_Lookup 验证注册后可按名称查找。
func TestRegistry_Register_Lookup(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(&stubPlatform{name: "matrix"})
	p, ok := r.Lookup("matrix")
	if !ok {
		t.Fatalf("Lookup(matrix) = false, want true")
	}
	if p.Name() != "matrix" {
		t.Fatalf("Name = %q, want matrix", p.Name())
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("Lookup(missing) = true, want false")
	}
}

// TestRegistry_Register_DuplicatePanics 验证同名重复注册触发 panic。
func TestRegistry_Register_DuplicatePanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("Register 重复名称未触发 panic")
		}
	}()
	r := NewRegistry()
	r.Register(&stubPlatform{name: "matrix"})
	r.Register(&stubPlatform{name: "matrix"})
}

// TestRegistry_Enabled 验证 terminal 始终启用、matrix 受配置开关控制。
func TestRegistry_Enabled(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(
		&stubPlatform{name: "terminal"},
		&stubPlatform{name: "matrix"},
	)

	t.Run("matrix_disabled", func(t *testing.T) {
		cfg := *config.DefaultConfig()
		cfg.Matrix.Enabled = false
		got := r.Enabled(&cfg)
		if len(got) != 1 {
			t.Fatalf("len(Enabled) = %d, want 1", len(got))
		}
		if got[0].Name() != "terminal" {
			t.Fatalf("Enabled[0] = %q, want terminal", got[0].Name())
		}
	})

	t.Run("matrix_enabled", func(t *testing.T) {
		cfg := *config.DefaultConfig()
		cfg.Matrix.Enabled = true
		got := r.Enabled(&cfg)
		if len(got) != 2 {
			t.Fatalf("len(Enabled) = %d, want 2", len(got))
		}
		// terminal 应排在 matrix 之前
		if got[0].Name() != "terminal" {
			t.Fatalf("Enabled[0] = %q, want terminal", got[0].Name())
		}
		if got[1].Name() != "matrix" {
			t.Fatalf("Enabled[1] = %q, want matrix", got[1].Name())
		}
	})

	t.Run("terminal_missing_skips_terminal", func(t *testing.T) {
		// 未注册 terminal 时即便 matrix 启用也不返回 terminal
		r2 := newTestRegistry(&stubPlatform{name: "matrix"})
		cfg := *config.DefaultConfig()
		cfg.Matrix.Enabled = true
		got := r2.Enabled(&cfg)
		if len(got) != 1 || got[0].Name() != "matrix" {
			t.Fatalf("Enabled = %v, want [matrix]", names(got))
		}
	})
}

// TestRegistry_Enabled_MatrixNotRegistered 验证配置启用但未注册时不返回 matrix。
func TestRegistry_Enabled_MatrixNotRegistered(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(&stubPlatform{name: "terminal"})
	cfg := *config.DefaultConfig()
	cfg.Matrix.Enabled = true
	got := r.Enabled(&cfg)
	if len(got) != 1 || got[0].Name() != "terminal" {
		t.Fatalf("Enabled = %v, want [terminal]", names(got))
	}
}

// TestRegistry_Enabled_ConfigDriven 验证启停完全由配置决定：新平台按开关注入、
// 未知平台不因注册而自动上线、terminal 也可以关闭。
func TestRegistry_Enabled_ConfigDriven(t *testing.T) {
	t.Parallel()
	r := newTestRegistry(
		&stubPlatform{name: "terminal"},
		&stubPlatform{name: "matrix"},
		&stubPlatform{name: "violet"},
		&stubPlatform{name: "discord"},
	)

	t.Run("only_violet", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Matrix.Enabled = false
		cfg.Platforms.Violet.Enabled = true
		got := r.Enabled(cfg)
		if len(got) != 2 || got[0].Name() != "terminal" || got[1].Name() != "violet" {
			t.Fatalf("Enabled = %v, want [terminal violet]（按注册顺序）", names(got))
		}
	})

	t.Run("terminal_disabled", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Platforms.Terminal.Enabled = false
		cfg.Matrix.Enabled = true
		got := r.Enabled(cfg)
		if len(got) != 1 || got[0].Name() != "matrix" {
			t.Fatalf("Enabled = %v, want [matrix]", names(got))
		}
	})

	t.Run("unknown_platform_never_starts", func(t *testing.T) {
		cfg := config.DefaultConfig()
		for _, name := range []string{"discord", ""} {
			cfg.Matrix.Enabled = true
			cfg.Platforms.Violet.Enabled = true
			for _, p := range r.Enabled(cfg) {
				if p.Name() == name {
					t.Fatalf("未登记开关的平台 %q 不应启动", name)
				}
			}
		}
	})
}

// names 提取平台名称便于断言失败信息。
func names(ps []Platform) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name()
	}
	return out
}
