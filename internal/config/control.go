package config

import "fmt"

// ControlConfig 控制任务图、持久澄清和显式交接；权限始终来自可信入口。
type ControlConfig struct {
	Enabled                bool `yaml:"enabled"`
	QuestionTimeoutSeconds int  `yaml:"question_timeout_seconds"`
	MaxPendingQuestions    int  `yaml:"max_pending_questions"`
	MaxDependencies        int  `yaml:"max_dependencies"`
	AllowCrossUserHandoff  bool `yaml:"allow_cross_user_handoff"`
}

// DefaultControlConfig 开启本用户任务控制，但不默认允许跨用户交接。
func DefaultControlConfig() ControlConfig {
	return ControlConfig{Enabled: true, QuestionTimeoutSeconds: 86400, MaxPendingQuestions: 20, MaxDependencies: 64}
}

// Validate 拒绝负数及无界任务控制配置。
func (c ControlConfig) Validate() error {
	if c.QuestionTimeoutSeconds < 0 || c.QuestionTimeoutSeconds > 604800 {
		return fmt.Errorf("control.question_timeout_seconds must be between 0 and 604800")
	}
	if c.MaxPendingQuestions < 0 || c.MaxPendingQuestions > 1000 {
		return fmt.Errorf("control.max_pending_questions must be between 0 and 1000")
	}
	if c.MaxDependencies < 0 || c.MaxDependencies > 256 {
		return fmt.Errorf("control.max_dependencies must be between 0 and 256")
	}
	return nil
}
