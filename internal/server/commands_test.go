package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/chat"
)

// stubCommands 记录本机指令分发，返回预设回执。
type stubCommands struct {
	got    chat.Session
	sender string
	id     string
	text   string
	reply  string
	handle bool
	err    error
}

func (s *stubCommands) Dispatch(_ context.Context, session chat.Session, senderID, id, text string) (string, bool, error) {
	s.got, s.sender, s.id, s.text = session, senderID, id, text
	return s.reply, s.handle, s.err
}

func commandsCall(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestServer_CommandDispatch(t *testing.T) {
	stub := &stubCommands{reply: "记忆条目：无", handle: true}
	h := New(nil, "secret", stub)
	recorder := commandsCall(t, h, "/v1/sessions/demo/commands", `{"id":"c1","text":"/memory list"}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "记忆条目：无")
	require.Equal(t, "demo", stub.got.Conversation)
	require.Equal(t, "terminal", stub.got.Platform)
	require.Equal(t, "/memory list", stub.text)
	require.Equal(t, "c1", stub.id)
	require.NotEmpty(t, stub.sender)
}

func TestServer_CommandInputValidation(t *testing.T) {
	stub := &stubCommands{handle: true}
	h := New(nil, "secret", stub)
	for _, tc := range []struct {
		name string
		path string
		body string
		want int
	}{
		{"无效会话编号", "/v1/sessions/bad.id/commands", `{"id":"c1","text":"/ping"}`, http.StatusBadRequest},
		{"缺少编号", "/v1/sessions/demo/commands", `{"id":"","text":"/ping"}`, http.StatusBadRequest},
		{"空指令", "/v1/sessions/demo/commands", `{"id":"c1","text":"  "}`, http.StatusBadRequest},
		{"多余字段", "/v1/sessions/demo/commands", `{"id":"c1","text":"/ping","room":"!x"}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, commandsCall(t, h, tc.path, tc.body).Code)
		})
	}
}

func TestServer_CommandNotHandledAndErrors(t *testing.T) {
	// 注册表未注册任何路径时命令未处理。
	h := New(nil, "secret", &stubCommands{handle: false})
	require.Equal(t, http.StatusBadRequest, commandsCall(t, h, "/v1/sessions/demo/commands", `{"id":"c1","text":"hello"}`).Code)

	// 分发失败时返回 500，不伪装成功。
	h = New(nil, "secret", &stubCommands{handle: true, err: errors.New("boom")})
	require.Equal(t, http.StatusInternalServerError, commandsCall(t, h, "/v1/sessions/demo/commands", `{"id":"c1","text":"/ping"}`).Code)
}

func TestServer_CommandWithoutDispatcher(t *testing.T) {
	h := New(nil, "secret")
	require.Equal(t, http.StatusServiceUnavailable, commandsCall(t, h, "/v1/sessions/demo/commands", `{"id":"c1","text":"/ping"}`).Code)
}
