package config

import "testing"

// TestAgentConfigMaxConcurrentTasks 验证并发任务数的默认值与范围，避免拼错的配置悄悄退回默认。
func TestAgentConfigMaxConcurrentTasks(t *testing.T) {
	cfg := DefaultAgentConfig()
	if cfg.MaxConcurrentTasks != DefaultMaxConcurrentTasks {
		t.Fatalf("默认并发任务数 = %d, want %d", cfg.MaxConcurrentTasks, DefaultMaxConcurrentTasks)
	}
	zero := DefaultAgentConfig()
	zero.MaxConcurrentTasks = 0
	if err := zero.Validate(); err != nil || zero.MaxConcurrentTasks != DefaultMaxConcurrentTasks {
		t.Fatalf("零值应回落到默认值: %v", err)
	}
	max := DefaultAgentConfig()
	max.MaxConcurrentTasks = MaxConcurrentTasksLimit
	if err := max.Validate(); err != nil {
		t.Fatalf("上限值被拒绝: %v", err)
	}
	for _, invalid := range []int{-1, MaxConcurrentTasksLimit + 1} {
		cfg := DefaultAgentConfig()
		cfg.MaxConcurrentTasks = invalid
		if err := cfg.Validate(); err == nil {
			t.Fatalf("非法并发任务数 %d 被接受", invalid)
		}
	}
}
