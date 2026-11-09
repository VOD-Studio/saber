package config

import (
	"fmt"
	"strings"
)

// HandoffConfig 只允许明确配置的同租户、同一实际用户会话交接；不合并记忆。
type HandoffConfig struct {
	Enabled bool                    `yaml:"enabled"`
	Routes  map[string]HandoffRoute `yaml:"routes"`
}

// HandoffEndpoint 是运营者配置的精确账号、会话及用户，不接受模型提供身份。
type HandoffEndpoint struct {
	Platform     string `yaml:"platform"`
	Account      string `yaml:"account"`
	Conversation string `yaml:"conversation"`
	Thread       string `yaml:"thread"`
	User         string `yaml:"user"`
	Tenant       string `yaml:"tenant"`
	Person       string `yaml:"person"`
	Direct       bool   `yaml:"direct"`
}

// HandoffRoute 只改变结果投递目的地，原任务和记忆身份保持不变。
type HandoffRoute struct {
	Source HandoffEndpoint `yaml:"source"`
	Target HandoffEndpoint `yaml:"target"`
}

// DefaultHandoffConfig 默认关闭跨平台会话交接。
func DefaultHandoffConfig() HandoffConfig {
	return HandoffConfig{Routes: make(map[string]HandoffRoute)}
}

// Validate 拒绝跨租户或没有精确用户映射的路由。
func (h HandoffConfig) Validate() error {
	for name, r := range h.Routes {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("handoff 路由名不能为空")
		}
		for _, endpoint := range []HandoffEndpoint{r.Source, r.Target} {
			if endpoint.Platform == "" || endpoint.Account == "" || endpoint.Conversation == "" || endpoint.User == "" || endpoint.Tenant == "" || endpoint.Person == "" {
				return fmt.Errorf("handoff.routes.%s: 两端必须精确配置 platform/account/conversation/user/tenant/person", name)
			}
		}
		if r.Source.Tenant != r.Target.Tenant || r.Source.Person != r.Target.Person {
			return fmt.Errorf("handoff.routes.%s: 禁止跨租户或跨实际用户交接", name)
		}
		if !r.Source.Direct || !r.Target.Direct {
			return fmt.Errorf("handoff.routes.%s: 群聊不能隐式迁移其他成员的上下文", name)
		}
		if r.Source.Platform == r.Target.Platform && r.Source.Account == r.Target.Account && r.Source.Conversation == r.Target.Conversation && r.Source.Thread == r.Target.Thread {
			return fmt.Errorf("handoff.routes.%s: 源和目标不能相同", name)
		}
	}
	return nil
}
