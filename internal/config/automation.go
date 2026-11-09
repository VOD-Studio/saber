package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// AutomationConfig 配置持久自动化；每次发生仍由任务管理器授权与执行。
type AutomationConfig struct {
	Enabled                 bool                             `yaml:"enabled" json:"enabled"`
	PollSeconds             int                              `yaml:"poll_seconds" json:"poll_seconds"`
	MinIntervalSeconds      int                              `yaml:"min_interval_seconds" json:"min_interval_seconds"`
	MaxTicks                int                              `yaml:"max_ticks" json:"max_ticks"`
	SelfPacedFloorSeconds   int                              `yaml:"self_paced_floor_seconds" json:"self_paced_floor_seconds"`
	SelfPacedCeilingSeconds int                              `yaml:"self_paced_ceiling_seconds" json:"self_paced_ceiling_seconds"`
	GoalMaxTurns            int                              `yaml:"goal_max_turns" json:"goal_max_turns"`
	JudgeModel              string                           `yaml:"judge_model" json:"judge_model"`
	JudgeTimeoutSeconds     int                              `yaml:"judge_timeout_seconds" json:"judge_timeout_seconds"`
	Hooks                   []AutomationHook                 `yaml:"hooks" json:"hooks"`
	Destinations            map[string]AutomationDestination `yaml:"destinations" json:"destinations"`
	Curator                 CuratorConfig                    `yaml:"curator" json:"curator"`
}

// AutomationHook 是管理员配置的工具或签名 HTTP 生命周期回调，不允许模型指定命令或 URL。
type AutomationHook struct {
	Name           string         `yaml:"name" json:"name"`
	Events         []string       `yaml:"events" json:"events"`
	Matcher        string         `yaml:"matcher" json:"matcher"`
	Tool           string         `yaml:"tool" json:"tool"`
	Args           map[string]any `yaml:"args" json:"args"`
	URL            string         `yaml:"url" json:"url"`
	SecretEnv      string         `yaml:"secret_env" json:"secret_env"`
	TimeoutSeconds int            `yaml:"timeout_seconds" json:"timeout_seconds"`
	FailClosed     bool           `yaml:"fail_closed" json:"fail_closed"`
}

// AutomationDestination 固定授权的投递路由；只允许已配置的工具和参数。
type AutomationDestination struct {
	Tool             string         `yaml:"tool" json:"tool"`
	Args             map[string]any `yaml:"args" json:"args"`
	Users            []string       `yaml:"users" json:"users"`
	ContentField     string         `yaml:"content_field" json:"content_field"`
	IdempotencyField string         `yaml:"idempotency_field" json:"idempotency_field"`
	External         bool           `yaml:"external" json:"external"`
}

// CuratorConfig 只维护显式收养的技能；自动清理从不永久删除。
type CuratorConfig struct {
	Enabled          bool    `yaml:"enabled" json:"enabled"`
	IntervalHours    int     `yaml:"interval_hours" json:"interval_hours"`
	MinIdleHours     float64 `yaml:"min_idle_hours" json:"min_idle_hours"`
	StaleAfterDays   int     `yaml:"stale_after_days" json:"stale_after_days"`
	ArchiveAfterDays int     `yaml:"archive_after_days" json:"archive_after_days"`
	Consolidate      bool    `yaml:"consolidate" json:"consolidate"`
	PruneBuiltins    bool    `yaml:"prune_builtins" json:"prune_builtins"`
	ArchiveTTLDays   int     `yaml:"archive_ttl_days" json:"archive_ttl_days"`
	BackupKeep       int     `yaml:"backup_keep" json:"backup_keep"`
}

// DefaultAutomationConfig 提供有界的自动化默认值，不隐式开放执行权限。
func DefaultAutomationConfig() AutomationConfig {
	return AutomationConfig{PollSeconds: 5, MinIntervalSeconds: 30, MaxTicks: 100, SelfPacedFloorSeconds: 60, SelfPacedCeilingSeconds: 900, GoalMaxTurns: 20, JudgeTimeoutSeconds: 30, Curator: CuratorConfig{Enabled: true, IntervalHours: 168, MinIdleHours: 2, StaleAfterDays: 14, ArchiveAfterDays: 30, BackupKeep: 2}}
}

// Validate 拒绝忙循环、无界超时和不明确的外部回调配置。
func (c AutomationConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.PollSeconds < 1 || c.PollSeconds > 60 || c.MinIntervalSeconds < 5 || c.MaxTicks < 0 || c.GoalMaxTurns < 1 || c.SelfPacedFloorSeconds < 10 || c.SelfPacedCeilingSeconds < c.SelfPacedFloorSeconds || c.JudgeTimeoutSeconds < 1 || c.JudgeTimeoutSeconds > 600 {
		return fmt.Errorf("自动化轮询/预算/间隔/评估超时无效")
	}
	seen := map[string]bool{}
	for _, h := range c.Hooks {
		if h.Name == "" || seen[h.Name] || len(h.Events) == 0 || (h.Tool == "") == (h.URL == "") {
			return fmt.Errorf("hook %q 必须有唯一名称、事件及一种回调", h.Name)
		}
		seen[h.Name] = true
		if h.TimeoutSeconds < 0 || h.TimeoutSeconds > 300 {
			return fmt.Errorf("hook %s 超时无效", h.Name)
		}
		if h.Matcher != "" {
			if _, err := regexp.Compile(h.Matcher); err != nil {
				return fmt.Errorf("hook %s matcher: %w", h.Name, err)
			}
		}
		for _, event := range h.Events {
			if strings.TrimSpace(event) == "" {
				return fmt.Errorf("hook %s 事件为空", h.Name)
			}
		}
		if h.URL != "" {
			u, err := url.Parse(h.URL)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("hook %s 需要 HTTPS URL", h.Name)
			}
			if h.SecretEnv == "" {
				return fmt.Errorf("hook %s 需要 secret_env 签名", h.Name)
			}
		}
	}
	for name, d := range c.Destinations {
		if name == "" || name == "origin" || name == "silent" || d.Tool == "" || len(d.Users) == 0 {
			return fmt.Errorf("投递 %q 需要工具和明确用户白名单", name)
		}
	}
	q := c.Curator
	if q.Enabled && (q.IntervalHours < 1 || q.MinIdleHours < 0 || q.StaleAfterDays < 1 || q.ArchiveAfterDays < q.StaleAfterDays || q.ArchiveTTLDays < 0 || q.BackupKeep < 1) {
		return fmt.Errorf("curator 时间或备份保留无效")
	}
	return nil
}
