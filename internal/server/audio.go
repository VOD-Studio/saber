package server

import (
	"net/http"
	"strings"

	"rua.plus/saber/internal/chat"
)

func (h *handler) audio(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Session string          `json:"session"`
		TaskID  int64           `json:"task_id"`
		Audio   chat.Attachment `json:"audio"`
		Text    string          `json:"text"`
		SDP     string          `json:"sdp"`
	}
	if err := decodeAPI(w, r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !identifier.MatchString(in.Session) {
		http.Error(w, "无效会话编号", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", in.Session)
	media := h.service.Media()
	if media == nil {
		http.Error(w, "媒体服务未启用", http.StatusServiceUnavailable)
		return
	}
	ctx := chat.WithIdentity(r.Context(), identity(r))
	if in.TaskID > 0 {
		bound, err := h.service.Tasks().BindToolContext(ctx, identity(r), in.TaskID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		ctx = bound
	}
	var out any
	var err error
	switch {
	case strings.HasSuffix(r.URL.Path, "transcriptions"):
		out, err = media.Transcribe(ctx, in.Audio)
	case strings.HasSuffix(r.URL.Path, "speech"):
		out, err = media.Synthesize(ctx, in.Text)
	case strings.HasSuffix(r.URL.Path, "live"):
		out, err = media.CreateLiveSession(ctx, in.SDP)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	respond(w, out)
}
