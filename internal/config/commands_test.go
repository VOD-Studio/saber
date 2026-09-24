package config

import (
	"testing"

	"rua.plus/saber/internal/chat"
)

func TestCommandPermissionsAreExactAndSeparate(t *testing.T) {
	cfg := CommandConfig{
		Admins:         []CommandAdmin{{Platform: "violet", Account: "site", Users: []string{"owner"}}},
		SessionWriters: []CommandSessionWriter{{Platform: "matrix", Account: "bot", Room: "room", Users: []string{"editor"}}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	admin := chat.Identity{Session: chat.Session{Platform: "violet", Account: "site", Conversation: "any"}, SenderID: "owner"}
	writer := chat.Identity{Session: chat.Session{Platform: "matrix", Account: "bot", Conversation: "room"}, SenderID: "editor"}
	if !cfg.IsAdmin(admin) || !cfg.CanWriteSession(admin) || cfg.IsAdmin(writer) || !cfg.CanWriteSession(writer) {
		t.Fatal("管理员与会话写入权限混淆")
	}
	for _, change := range []func(*chat.Identity){
		func(i *chat.Identity) { i.Session.Platform = "violet" },
		func(i *chat.Identity) { i.Session.Account = "other" },
		func(i *chat.Identity) { i.Session.Conversation = "other" },
		func(i *chat.Identity) { i.Session.Thread = "topic" },
		func(i *chat.Identity) { i.SenderID = "other" },
	} {
		other := writer
		change(&other)
		if cfg.CanWriteSession(other) {
			t.Fatalf("跨身份会话获得写入权限：%+v", other)
		}
	}
	cfg.Admins[0].Users = []string{"*"}
	if cfg.Validate() == nil {
		t.Fatal("全局命令管理员接受了通配符")
	}
}
