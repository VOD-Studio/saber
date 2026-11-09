package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func audioProxy(endpoint, token, origin string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != origin {
			http.Error(w, "跨源音频连接被拒绝", http.StatusForbidden)
			return
		}
		kind := r.URL.Query().Get("kind")
		if kind != "transcriptions" && kind != "speech" {
			http.Error(w, "无效音频流", 400)
			return
		}
		taskID, err := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
		if err != nil || taskID <= 0 {
			http.Error(w, "需要持久运行任务", 400)
			return
		}
		conversation := r.URL.Query().Get("session")
		if conversation == "" || len(conversation) > 128 || strings.ContainsAny(conversation, "/\\\r\n") {
			http.Error(w, "无效会话", 400)
			return
		}
		target, err := url.Parse(endpoint)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if target.Scheme == "https" {
			target.Scheme = "wss"
		} else {
			target.Scheme = "ws"
		}
		target.Path = "/v1/audio/" + kind + "/stream"
		target.RawQuery = url.Values{"session": {conversation}, "task_id": {strconv.FormatInt(taskID, 10)}}.Encode()
		dialCtx, cancelDial := context.WithTimeout(r.Context(), 15*time.Second)
		upstream, _, err := websocket.Dial(dialCtx, target.String(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
		cancelDial()
		if err != nil {
			http.Error(w, fmt.Sprintf("音频服务连接失败: %v", err), http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.CloseNow() }()
		upstream.SetReadLimit(32 << 20)
		browser, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = browser.CloseNow() }()
		browser.SetReadLimit(262144)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		done := make(chan error, 2)
		go func() {
			for {
				kind, data, e := browser.Read(ctx)
				if e != nil {
					done <- e
					return
				}
				var frame struct {
					Type string `json:"type"`
				}
				if kind != websocket.MessageText || json.Unmarshal(data, &frame) != nil || (frame.Type != "pcm" && frame.Type != "commit" && frame.Type != "text" && frame.Type != "end") {
					done <- fmt.Errorf("无效音频帧")
					return
				}
				if e = upstream.Write(ctx, websocket.MessageText, data); e != nil {
					done <- e
					return
				}
			}
		}()
		go func() {
			for {
				kind, data, e := upstream.Read(ctx)
				if e != nil {
					done <- e
					return
				}
				if e = browser.Write(ctx, kind, data); e != nil {
					done <- e
					return
				}
			}
		}()
		<-done
		cancel()
		_ = browser.Close(websocket.StatusNormalClosure, "")
		_ = upstream.Close(websocket.StatusNormalClosure, "")
	}
}
