package bot

import (
	"context"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/command"
)

// localCommandAdapter 捕获共享命令的输出文本，供本机接口作为回执回传。
type localCommandAdapter struct{ text string }

func (a *localCommandAdapter) Capabilities() chat.Capabilities { return chat.Capabilities{} }

func (a *localCommandAdapter) Send(_ context.Context, reply chat.Reply) (string, error) {
	a.text += reply.Text
	return "local", nil
}

func (a *localCommandAdapter) Edit(_ context.Context, _ string, reply chat.Reply) error {
	a.text += reply.Text
	return nil
}

func (a *localCommandAdapter) SetTyping(context.Context, chat.Session, bool) error { return nil }

// localCommands 把本机终端已认证的指令交给共享命令注册表，复用同一套语义与授权。
//
// 身份由服务端令牌与操作系统用户确定：本机入口是受信的单用户私聊，
// 因此消息标记为 Direct，使仅限会话写入者的命令（清上下文、人格、记忆修改）对本机操作者生效。
type localCommands struct {
	registry     *command.Registry
	capabilities func(platform string) map[string]bool
}

// Dispatch 执行一条本机指令并返回回执文本。
func (l localCommands) Dispatch(ctx context.Context, session chat.Session, senderID, id, text string) (string, bool, error) {
	if l.registry == nil {
		return "", false, nil
	}
	caps := map[string]bool{}
	if l.capabilities != nil {
		caps = l.capabilities(session.Platform)
	}
	adapter := &localCommandAdapter{}
	message := chat.Message{Session: session, ID: id, SenderID: senderID, Direct: true, Text: text}
	handled, err := l.registry.Dispatch(ctx, message, adapter, caps)
	return adapter.text, handled, err
}

// 确保本机命令分发实现服务端接口。
var _ interface {
	Dispatch(context.Context, chat.Session, string, string, string) (string, bool, error)
} = localCommands{}
