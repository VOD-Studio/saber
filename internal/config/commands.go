package config

import (
	"errors"
	"strings"

	"rua.plus/saber/internal/chat"
)

// CommandConfig 将全局命令管理员与会话写入权限独立于任务管理员配置。
type CommandConfig struct {
	Admins               []CommandAdmin         `yaml:"admins"`
	SessionWriters       []CommandSessionWriter `yaml:"session_writers"`
	LegacyPersonaAccount string                 `yaml:"legacy_persona_account"`
}

// CommandAdmin 在一个平台账号内授权全局命令，不继承站点或任务管理权限。
type CommandAdmin struct {
	Platform string   `yaml:"platform"`
	Account  string   `yaml:"account"`
	Users    []string `yaml:"users"`
}

// CommandSessionWriter 只允许修改一个完整会话；空 thread 指主会话。
type CommandSessionWriter struct {
	Platform string   `yaml:"platform"`
	Account  string   `yaml:"account"`
	Room     string   `yaml:"room"`
	Thread   string   `yaml:"thread"`
	Users    []string `yaml:"users"`
}

// Validate 拒绝空标识和通配符，避免误授全局操作权限。
func (c CommandConfig) Validate() error {
	for _, entry := range c.Admins {
		if entry.Platform == "" || entry.Account == "" || len(entry.Users) == 0 || strings.Contains(entry.Platform+entry.Account, "*") {
			return errors.New("command admin requires exact platform, account and users")
		}
		for _, user := range entry.Users {
			if user == "" || strings.Contains(user, "*") {
				return errors.New("command admin requires exact user IDs")
			}
		}
	}
	for _, entry := range c.SessionWriters {
		if entry.Platform == "" || entry.Account == "" || entry.Room == "" || len(entry.Users) == 0 || strings.Contains(entry.Platform+entry.Account+entry.Room+entry.Thread, "*") {
			return errors.New("session writer requires exact platform, account, room and users")
		}
		for _, user := range entry.Users {
			if user == "" || strings.Contains(user, "*") {
				return errors.New("session writer requires exact user IDs")
			}
		}
	}
	return nil
}

// IsAdmin 只使用接入端给出的完整身份检查全局授权。
func (c CommandConfig) IsAdmin(identity chat.Identity) bool {
	if identity.Session.Validate() != nil || identity.SenderID == "" {
		return false
	}
	for _, entry := range c.Admins {
		if entry.Platform == identity.Session.Platform && entry.Account == identity.Session.Account {
			for _, user := range entry.Users {
				if user == identity.SenderID {
					return true
				}
			}
		}
	}
	return false
}

// CanWriteSession 检查精确会话授权，全局命令管理员也可修改。
func (c CommandConfig) CanWriteSession(identity chat.Identity) bool {
	if c.IsAdmin(identity) {
		return true
	}
	for _, entry := range c.SessionWriters {
		if entry.Platform == identity.Session.Platform && entry.Account == identity.Session.Account && entry.Room == identity.Session.Conversation && entry.Thread == identity.Session.Thread {
			for _, user := range entry.Users {
				if user == identity.SenderID {
					return true
				}
			}
		}
	}
	return false
}
