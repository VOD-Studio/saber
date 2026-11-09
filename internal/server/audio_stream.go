package server

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
	"rua.plus/saber/internal/chat"
)

type voiceFrame struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Text string `json:"text,omitempty"`
}

func (h *handler) audioStream(w http.ResponseWriter, r *http.Request) {
	s := r.URL.Query().Get("session")
	id, err := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
	if !identifier.MatchString(s) || err != nil || id <= 0 {
		http.Error(w, "需要会话和运行中任务编号", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", s)
	trusted := identity(r)
	bound, err := h.service.Tasks().BindToolContext(r.Context(), trusted, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	media := h.service.Media()
	if media == nil {
		http.Error(w, "媒体服务未启用", http.StatusServiceUnavailable)
		return
	}
	socket := websocket.Server{Handshake: func(cfg *websocket.Config, r *http.Request) error { return nil }, Handler: func(ws *websocket.Conn) {
		ctx, cancel := context.WithCancel(bound)
		defer cancel()
		defer func() { _ = ws.Close() }()
		ws.MaxPayloadBytes = 1 << 20
		done := make(chan struct{})
		defer close(done)
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					_ = ws.Close()
					return
				case <-ticker.C:
					if _, e := h.service.Tasks().BindToolContext(ctx, trusted, id); e != nil {
						cancel()
						_ = ws.Close()
						return
					}
				}
			}
		}()
		var writer sync.Mutex
		send := func(v any) error {
			writer.Lock()
			defer writer.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := ws.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return err
			}
			return websocket.JSON.Send(ws, v)
		}
		report := func(e error) {
			if e != nil && !errors.Is(e, context.Canceled) {
				_ = send(map[string]any{"type": "error", "error": e.Error()})
			}
		}
		if strings.HasSuffix(r.URL.Path, "transcriptions/stream") {
			stream, e := media.StartTranscription(ctx)
			if e != nil {
				report(e)
				return
			}
			defer func() { _ = stream.Close() }()
			eventsDone := make(chan struct{})
			go func() {
				defer close(eventsDone)
				for {
					select {
					case <-ctx.Done():
						return
					case event, ok := <-stream.Events():
						if !ok {
							return
						}
						if send(event) != nil {
							cancel()
							return
						}
					}
				}
			}()
			defer func() { cancel(); <-eventsDone }()
			for {
				var frame voiceFrame
				if e = websocket.JSON.Receive(ws, &frame); e != nil {
					cancel()
					return
				}
				switch frame.Type {
				case "pcm":
					pcm, e := base64.StdEncoding.DecodeString(frame.Data)
					if e != nil {
						report(errors.New("无效 PCM base64"))
						return
					}
					if e = stream.WritePCM(pcm); e != nil {
						report(e)
						return
					}
				case "commit":
					transcript, e := stream.Finish()
					if e != nil {
						report(e)
						return
					}
					<-eventsDone
					report(send(map[string]any{"type": "completed", "transcript": transcript}))
					return
				case "cancel":
					cancel()
					return
				default:
					report(errors.New("仅接受 pcm、commit、cancel 音频帧"))
					return
				}
			}
		}
		deltas := make(chan string, 32)
		speechDone := make(chan error, 1)
		go func() {
			speechDone <- media.SpeechStream(ctx, deltas, func(audio chat.Attachment) error { return send(map[string]any{"type": "audio", "audio": audio}) })
		}()
		frames := make(chan voiceFrame)
		readErr := make(chan error, 1)
		go func() {
			for {
				var frame voiceFrame
				if e := websocket.JSON.Receive(ws, &frame); e != nil {
					readErr <- e
					return
				}
				select {
				case frames <- frame:
				case <-ctx.Done():
					return
				}
			}
		}()
		defer func() { cancel(); _ = ws.Close() }()
		ended := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-readErr:
				cancel()
				return
			case e := <-speechDone:
				if e != nil {
					report(e)
				} else {
					report(send(map[string]any{"type": "completed"}))
				}
				return
			case frame := <-frames:
				if ended {
					report(errors.New("文本流已经结束"))
					return
				}
				switch frame.Type {
				case "text":
					select {
					case deltas <- frame.Text:
					case e := <-speechDone:
						report(e)
						return
					case <-ctx.Done():
						return
					}
				case "end":
					ended = true
					close(deltas)
				case "cancel":
					cancel()
					return
				default:
					report(errors.New("仅接受 text、end、cancel 语音帧"))
					return
				}
			}
		}
	}}
	socket.ServeHTTP(w, r)
}

func (h *handler) wake(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Session string          `json:"session"`
		TaskID  int64           `json:"task_id"`
		Audio   chat.Attachment `json:"audio"`
	}
	if decodeAPI(w, r, &in) != nil || !identifier.MatchString(in.Session) || in.TaskID <= 0 {
		http.Error(w, "需要会话、运行中任务和真实音频", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", in.Session)
	ctx, err := h.service.Tasks().BindToolContext(r.Context(), identity(r), in.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	media := h.service.Media()
	if media == nil {
		http.Error(w, "媒体服务未启用", http.StatusServiceUnavailable)
		return
	}
	detected, err := media.DetectWake(ctx, in.Audio)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	respond(w, map[string]bool{"detected": detected})
}
