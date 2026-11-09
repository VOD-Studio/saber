package telegramplatform

// Account 返回可信配置的 Telegram 账号作用域。
func (p *Platform) Account() string { return p.cfg.EffectiveAccount() }

// Enabled 判断当前账号是否被运营者显式启用。
func (p *Platform) Enabled() bool { return p.cfg.Enabled }
