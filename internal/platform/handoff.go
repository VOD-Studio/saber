package platform

import (
	"context"
	"errors"
	"fmt"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// HandoffResolver 将显式路由名解析为可信目标；任务层负责持久化和派发前再次调用。
func HandoffResolver(cfg config.HandoffConfig, registry *Registry) func(context.Context, chat.Identity, string) (chat.Identity, error) {
	return func(ctx context.Context, source chat.Identity, route string) (chat.Identity, error) {
		if err := ctx.Err(); err != nil {
			return chat.Identity{}, err
		}
		if !cfg.Enabled {
			return chat.Identity{}, errors.New("跨平台交接未启用")
		}
		if err := cfg.Validate(); err != nil {
			return chat.Identity{}, err
		}
		r, ok := cfg.Routes[route]
		if !ok {
			return chat.Identity{}, errors.New("未授权的交接路由")
		}
		expected := handoffIdentity(r.Source)
		if source != expected {
			return chat.Identity{}, errors.New("交接来源不匹配可信用户映射")
		}
		target := handoffIdentity(r.Target)
		if registry == nil {
			return chat.Identity{}, errors.New("交接缺少平台注册表")
		}
		p, ok := registry.Lookup(target.Session.Platform)
		if !ok {
			return chat.Identity{}, fmt.Errorf("交接目标平台 %s 未注册", target.Session.Platform)
		}
		if _, ok := p.(TaskDelivery); !ok {
			return chat.Identity{}, errors.New("交接目标不支持任务结果投递")
		}
		scoped, ok := p.(interface {
			Account() string
			Enabled() bool
		})
		if !ok || scoped.Account() != target.Session.Account || !scoped.Enabled() {
			return chat.Identity{}, errors.New("交接目标账号不匹配或未启用")
		}
		return target, nil
	}
}

func handoffIdentity(e config.HandoffEndpoint) chat.Identity {
	return chat.Identity{Session: chat.Session{Platform: e.Platform, Account: e.Account, Conversation: e.Conversation, Thread: e.Thread}, SenderID: e.User, Direct: e.Direct}
}
