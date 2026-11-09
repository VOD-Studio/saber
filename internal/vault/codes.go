package vault

import (
	"context"
	"errors"
	"os"
	"strings"
)

// EnterCode 消费可信 UI 的遮罩验证码，不保存或经模型转发，并绑定正在显示的精确站点。
func (s *Store) EnterCode(ctx context.Context, session, origin, code string) error {
	if _, err := owner(ctx); err != nil {
		return err
	}
	if !s.cfg.Enabled || s.browser == nil {
		return errors.New("supervised browser/vault unavailable")
	}
	if len(code) < 1 || len(code) > 64 || strings.ContainsAny(code, "\x00\r\n") {
		return errors.New("verification code must be 1..64 characters without control lines")
	}
	if s.host.Check == nil {
		return errors.New("browser authorization unavailable")
	}
	if err := s.host.Check(ctx, "browser_exec", true); err != nil {
		return err
	}
	if err := s.host.Check(ctx, "exec", false); err != nil {
		return err
	}
	expected, err := NormalizeOrigin(origin)
	if err != nil {
		return err
	}
	current, err := s.browser.Origin(ctx, session)
	if err != nil {
		return err
	}
	current, err = NormalizeOrigin(current)
	if err != nil {
		return err
	}
	if current != expected {
		return errors.New("verification page navigated or origin changed")
	}
	fields := map[string]string{"one_time_code": code}
	s.remember(fields)
	if err := s.browser.Fill(ctx, session, expected, fields); err != nil {
		return errors.New(s.Redact(err.Error()))
	}
	return nil
}

// Sources 返回当前用户实际启用、授权及解锁状态，绝不返回密码管理器 token。
func (s *Store) Sources(ctx context.Context) (map[string]map[string]bool, error) {
	who, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	disabled := map[string]bool{}
	if err := s.transaction(ctx, false, func(state *diskState) error {
		for key, enabled := range state.Sources {
			disabled[key] = !enabled
		}
		return nil
	}); err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, name := range []string{"onepassword", "bitwarden"} {
		enabled := s.managerAllowed(name, who) && !disabled[who+":"+name]
		cfg := s.managerConfig(name)
		unlocked := s.managerToken(who, name) != ""
		if cfg.TokenEnv != "" {
			unlocked = unlocked || os.Getenv(cfg.TokenEnv) != ""
		}
		out[name] = map[string]bool{"enabled": enabled, "unlocked": enabled && unlocked}
	}
	return out, nil
}
