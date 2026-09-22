package matrixplatform_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	platformmatrix "rua.plus/saber/internal/platform/matrix"
)

// newPlatform 构造一个仅用于规范化的 Matrix 平台：模型服务器不会被触达，
// 因为测试注入的 runner 只记录收到的消息。
func newPlatform(t *testing.T, run platformmatrix.ChatRunner, edit bool) (*platformmatrix.Platform, *config.Config) {
	t.Helper()
	client, err := mautrix.NewClient("http://unused.invalid", "@bot:local", "test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Matrix.StreamEdit.Enabled = edit
	return platformmatrix.New(cfg, matrix.NewCommandService(client, id.UserID("@bot:local"), &matrix.BuildInfo{}), nil, run), cfg
}

// recordingRunner 返回记录入参的 runner 与读取函数。
func recordingRunner() (platformmatrix.ChatRunner, func() (chat.Message, chat.Adapter, string)) {
	var (
		gotMessage  chat.Message
		gotAdapter  chat.Adapter
		gotModel    string
		called      bool
		errToReturn error
	)
	return func(ctx context.Context, message chat.Message, adapter chat.Adapter, modelName string) (agent.Result, error) {
			gotMessage, gotAdapter, gotModel, called = message, adapter, modelName, true
			return agent.Result{}, errToReturn
		}, func() (chat.Message, chat.Adapter, string) {
			if !called {
				panic("runner 未被调用")
			}
			return gotMessage, gotAdapter, gotModel
		}
}

func TestPlatformLifecycle(t *testing.T) {
	noop := func(context.Context, chat.Message, chat.Adapter, string) (agent.Result, error) {
		return agent.Result{}, nil
	}
	tests := []struct {
		name     string
		commands bool
		start    chat.Handler
		wantErr  string
	}{
		{name: "启动后记录共享 handler", commands: true, start: func(context.Context, chat.Message, chat.Adapter) (agent.Result, error) { return agent.Result{}, nil }},
		{name: "缺少命令服务时启动失败", commands: false, wantErr: "matrix 平台缺少命令服务"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, cfg := newPlatform(t, noop, true)
			if !tt.commands {
				p = platformmatrix.New(cfg, nil, nil, noop)
			}
			if got := p.Name(); got != "matrix" {
				t.Fatalf("Name() = %q", got)
			}
			err := p.Start(context.Background(), tt.start)
			switch {
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Start() 应返回错误 %q", tt.wantErr)
			case tt.wantErr != "":
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Start() 错误 = %v, 期望包含 %q", err, tt.wantErr)
				}
				return
			case err != nil:
				t.Fatalf("Start() 返回错误: %v", err)
			}
			p.Stop()
			if err := p.HandleCommand(context.Background(), "@user:local", "!room:local", []string{"你好"}, "gpt-4"); err != nil {
				t.Fatalf("HandleCommand() 返回错误: %v", err)
			}
		})
	}
}

// TestPlatformHandleCommandNormalizes 验证入站 Matrix 命令被规范化为通用消息，
// 并按调用方指定的模型执行。
func TestPlatformHandleCommandNormalizes(t *testing.T) {
	run, read := recordingRunner()
	p, _ := newPlatform(t, run, true)
	if err := p.Start(context.Background(), func(context.Context, chat.Message, chat.Adapter) (agent.Result, error) {
		return agent.Result{}, errors.New("不应走默认入口")
	}); err != nil {
		t.Fatal(err)
	}
	ctx := matrix.WithMessageRelations(matrix.WithEventID(context.Background(), "$event"), "$parent", "$thread")
	if err := p.HandleCommand(ctx, "@user:local", "!room:local", []string{"帮我", "写代码"}, "openai.gpt-4"); err != nil {
		t.Fatal(err)
	}
	message, adapter, model := read()
	if model != "openai.gpt-4" {
		t.Fatalf("modelName = %q", model)
	}
	if message.Text != "帮我 写代码" || message.ID != "$event" || message.SenderID != "@user:local" {
		t.Fatalf("消息规范化失败: %+v", message)
	}
	if message.Session != (chat.Session{Platform: "matrix", Account: "@bot:local", Conversation: "!room:local", Thread: "$thread"}) {
		t.Fatalf("会话作用域失败: %+v", message.Session)
	}
	if message.ReplyTo != "$parent" {
		t.Fatalf("ReplyTo = %q", message.ReplyTo)
	}
	if caps := adapter.Capabilities(); !caps.Edit || !caps.Reply || !caps.Typing {
		t.Fatalf("聊天 adapter 能力缺失: %+v", caps)
	}
}

// TestPlatformHandleCommandDefaultModel 验证未指定模型时复用 Start 注入的通用入口。
func TestPlatformHandleCommandDefaultModel(t *testing.T) {
	var seen string
	p, _ := newPlatform(t, func(context.Context, chat.Message, chat.Adapter, string) (agent.Result, error) {
		seen = "runner"
		return agent.Result{}, nil
	}, true)
	if err := p.Start(context.Background(), func(_ context.Context, message chat.Message, _ chat.Adapter) (agent.Result, error) {
		seen = "handler:" + message.Text
		return agent.Result{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.HandleCommand(context.Background(), "@user:local", "!room:local", []string{"默认模型"}, ""); err != nil {
		t.Fatal(err)
	}
	if seen != "handler:默认模型" {
		t.Fatalf("默认模型路径未复用共享 handler: %q", seen)
	}
}

// TestPlatformUnwired 覆盖未绑定聊天链路与未启动两种失败：平台不能替核心臆造模型。
func TestPlatformUnwired(t *testing.T) {
	cfg := config.DefaultConfig()
	p := platformmatrix.New(cfg, nil, nil, nil)
	if err := p.HandleCommand(context.Background(), "@user:local", "!room:local", nil, "m"); err == nil {
		t.Fatal("未绑定聊天链路时仍接受了命令")
	}
	p, _ = newPlatform(t, func(context.Context, chat.Message, chat.Adapter, string) (agent.Result, error) {
		return agent.Result{}, nil
	}, true)
	if err := p.HandleCommand(context.Background(), "@user:local", "!room:local", nil, "m"); err == nil {
		t.Fatal("未启动时仍接受了命令")
	}
}

// TestPlatformAdapters 区分聊天展示与任务投递两种 adapter 的编辑能力。
func TestPlatformAdapters(t *testing.T) {
	p, cfg := newPlatform(t, nil, true)
	if got := p.ChatAdapter(nil).Capabilities(); !got.Edit {
		t.Fatalf("聊天 adapter 应支持编辑: %+v", got)
	}
	if got := p.DeliveryAdapter().Capabilities(); got.Edit {
		t.Fatalf("投递 adapter 不应流式编辑: %+v", got)
	}
	cfg.Matrix.StreamEdit.Enabled = false
	if got := p.ChatAdapter(nil).Capabilities(); got.Edit {
		t.Fatalf("关闭流式编辑后聊天 adapter 仍声明支持: %+v", got)
	}
}

// TestPlatformDeliveryAdapterSends 确认投递 adapter 复用 Matrix 的发送与幂等事务。
func TestPlatformDeliveryAdapterSends(t *testing.T) {
	var (
		sent *event.MessageEventContent
		path string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body := &event.MessageEventContent{}
		if err := json.NewDecoder(r.Body).Decode(body); err != nil {
			t.Error(err)
		}
		sent = body
		if _, err := fmt.Fprintf(w, `{"event_id":"$delivered"}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := mautrix.NewClient(server.URL, "@bot:local", "test")
	if err != nil {
		t.Fatal(err)
	}
	platform := platformmatrix.New(config.DefaultConfig(), matrix.NewCommandService(client, id.UserID("@bot:local"), &matrix.BuildInfo{}), nil, nil)
	messageID, err := platform.DeliveryAdapter().Send(context.Background(), chat.Reply{
		Session:       chat.Session{Platform: "matrix", Account: "@bot:local", Conversation: "!room:local"},
		Text:          "任务完成",
		TransactionID: "txn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "$delivered" || sent == nil || sent.Body != "任务完成" {
		t.Fatalf("投递失败: id=%q body=%+v", messageID, sent)
	}
	// r.URL.Path 已解码，事务 ID 出现在 send 路径末尾即证明复用了幂等事务。
	if !strings.Contains(path, "/rooms/!room:local/send/m.room.message/txn-1") {
		t.Fatalf("未使用幂等事务发送: %s", path)
	}
}

// TestPlatformNormalizeCommand 验证只读命令复用同一套会话作用域，且回执 adapter 不做流式编辑。
func TestPlatformNormalizeCommand(t *testing.T) {
	p, _ := newPlatform(t, nil, true)
	ctx := matrix.WithMessageRelations(matrix.WithEventID(context.Background(), "$cmd"), "$parent", "$thread")
	message, adapter, err := p.NormalizeCommand(ctx, "@user:local", "!room:local", "!task list")
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "!task list" || message.ID != "$cmd" || message.ReplyTo != "$parent" {
		t.Fatalf("命令规范化失败: %+v", message)
	}
	if message.Session.Conversation != "!room:local" || message.Session.Platform != "matrix" {
		t.Fatalf("会话作用域失败: %+v", message.Session)
	}
	if got := adapter.Capabilities(); got.Edit {
		t.Fatalf("只读命令回执不应流式编辑: %+v", got)
	}
	if _, _, err := platformmatrix.New(config.DefaultConfig(), nil, nil, nil).NormalizeCommand(ctx, "@user:local", "!room:local", "!task list"); err == nil {
		t.Fatal("缺少命令服务时仍规范化了命令")
	}
}

// TestPlatformSession 验证会话作用域由平台解析：账号、房间与线程缺一不可。
func TestPlatformSession(t *testing.T) {
	p, _ := newPlatform(t, nil, true)
	ctx := matrix.WithMessageRelations(context.Background(), "", "$thread")
	want := chat.Session{Platform: "matrix", Account: "@bot:local", Conversation: "!room:local", Thread: "$thread"}
	if got := p.Session(ctx, id.RoomID("!room:local")); got != want {
		t.Fatalf("Session() = %+v, 期望 %+v", got, want)
	}
	empty := platformmatrix.New(config.DefaultConfig(), nil, nil, nil)
	if got := empty.Session(ctx, id.RoomID("!room:local")); got.Validate() == nil {
		t.Fatalf("缺少命令服务时不应给出有效会话: %+v", got)
	}
}
