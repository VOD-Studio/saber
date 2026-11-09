package matrixplatform

// Account 返回已登录 Matrix bot 的真实用户 ID。
func (p *Platform) Account() string {
	if p.commands == nil {
		return ""
	}
	return string(p.commands.BotID())
}

// Enabled 判断当前账号是否被运营者显式启用。
func (p *Platform) Enabled() bool { return p.cfg != nil && p.cfg.Platforms.Matrix.Enabled }
