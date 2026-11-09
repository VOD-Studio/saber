package server

import (
	"net/http"

	"rua.plus/saber/internal/chat"
)

func (h *handler) startAudioSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Session string `json:"session"`
		ID      string `json:"id"`
	}
	if decodeAPI(w, r, &in) != nil || !identifier.MatchString(in.Session) {
		http.Error(w, "需要有效会话编号", http.StatusBadRequest)
		return
	}
	if in.ID == "" {
		in.ID = newChatCompletionID()
	}
	if !identifier.MatchString(in.ID) {
		http.Error(w, "无效消息编号", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", in.Session)
	message := chat.Message{Session: session(r), SenderID: identity(r).SenderID, ID: in.ID, Direct: true, Text: "音频会话"}
	trusted := identity(r)
	trusted.Direct = true
	t, err := h.service.StartVoiceTask(chat.WithIdentity(r.Context(), trusted), message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	respond(w, h.view(t))
}
func (h *handler) finishAudioSession(w http.ResponseWriter, r *http.Request) {
	s := r.URL.Query().Get("session")
	if !identifier.MatchString(s) {
		http.Error(w, "需要会话编号", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", s)
	id, err := taskID(r)
	if err != nil {
		http.Error(w, "无效任务编号", http.StatusBadRequest)
		return
	}
	t, err := h.service.Tasks().FinishExternal(r.Context(), identity(r), id, "completed", "音频会话已关闭")
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	respond(w, h.view(t))
}
