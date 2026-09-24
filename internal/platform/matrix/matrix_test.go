package matrixplatform_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	platformmatrix "rua.plus/saber/internal/platform/matrix"
)

func TestPlatformLifecycleAndAdapters(t *testing.T) {
	cfg := config.DefaultConfig()
	missing := platformmatrix.New(cfg, nil, nil)
	if err := missing.Start(context.Background(), nil); err == nil {
		t.Fatal("缺少命令服务时应拒绝启动")
	}
	client, err := mautrix.NewClient("http://unused.invalid", "@bot:local", "test")
	if err != nil {
		t.Fatal(err)
	}
	p := platformmatrix.New(cfg, matrix.NewCommandService(client, "@bot:local", nil), nil)
	if p.Name() != "matrix" {
		t.Fatal("平台名称错误")
	}
	if err := p.Start(context.Background(), func(context.Context, chat.Message, chat.Adapter) (agent.Result, error) { return agent.Result{}, nil }); err != nil {
		t.Fatal(err)
	}
	if !p.ChatAdapter(nil).Capabilities().Edit || !p.DeliveryAdapter().Capabilities().Edit {
		t.Fatal("流式编辑能力没有传给 Matrix adapter")
	}
	cfg.Matrix.StreamEdit.Enabled = false
	if p.ChatAdapter(nil).Capabilities().Edit || p.DeliveryAdapter().Capabilities().Edit {
		t.Fatal("关闭编辑后 adapter 仍声明支持")
	}
	p.Stop()
}

func TestPlatformDeliveryAdapterSends(t *testing.T) {
	var sent *event.MessageEventContent
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body := &event.MessageEventContent{}
		if err := json.NewDecoder(r.Body).Decode(body); err != nil {
			t.Error(err)
		}
		sent = body
		if _, err := fmt.Fprint(w, `{"event_id":"$delivered"}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := mautrix.NewClient(server.URL, "@bot:local", "test")
	if err != nil {
		t.Fatal(err)
	}
	p := platformmatrix.New(config.DefaultConfig(), matrix.NewCommandService(client, "@bot:local", nil), nil)
	id, err := p.DeliveryAdapter().Send(context.Background(), chat.Reply{Session: chat.Session{Platform: "matrix", Account: "@bot:local", Conversation: "!room:local"}, Text: "任务完成", TransactionID: "txn-1"})
	if err != nil || id != "$delivered" || sent == nil || sent.Body != "任务完成" || !strings.Contains(path, "/rooms/!room:local/send/m.room.message/txn-1") {
		t.Fatalf("投递失败: id=%q body=%+v path=%q err=%v", id, sent, path, err)
	}
}
