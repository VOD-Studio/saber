package server

import (
	"net/http"

	"rua.plus/saber/internal/chat"
)

func (h *handler) vaultSources(w http.ResponseWriter, r *http.Request) {
	store := h.service.Vault()
	if store == nil {
		http.Error(w, "Vault 未启用", http.StatusServiceUnavailable)
		return
	}
	r.SetPathValue("session", "private-vault")
	who := identity(r)
	who.Direct = true
	out, err := store.Sources(chat.WithIdentity(r.Context(), who))
	if err != nil {
		http.Error(w, "密码管理器状态不可用", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, out)
}
func (h *handler) vaultCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Conversation string `json:"conversation"`
		Session      string `json:"session"`
		TaskID       int64  `json:"task_id"`
		Origin       string `json:"origin"`
		Code         string `json:"code"`
	}
	if decodeAPI(w, r, &in) != nil || !identifier.MatchString(in.Conversation) || in.TaskID <= 0 || in.Code == "" {
		http.Error(w, "需要会话、任务、浏览器站点和验证码", http.StatusBadRequest)
		return
	}
	r.SetPathValue("session", in.Conversation)
	ctx, err := h.service.Tasks().BindToolContext(r.Context(), identity(r), in.TaskID)
	if err != nil {
		http.Error(w, "当前任务无权输入验证码", http.StatusForbidden)
		return
	}
	store := h.service.Vault()
	if store == nil {
		http.Error(w, "Vault 未启用", http.StatusServiceUnavailable)
		return
	}
	err = store.EnterCode(ctx, in.Session, in.Origin, in.Code)
	in.Code = ""
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		http.Error(w, "验证码输入失败：任务权限、站点或页面不匹配", http.StatusForbidden)
		return
	}
	respond(w, map[string]bool{"entered": true})
}
