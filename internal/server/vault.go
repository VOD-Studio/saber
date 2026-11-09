package server

import (
	"net/http"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/vault"
)

func (h *handler) vaultAPI(w http.ResponseWriter, r *http.Request) {
	store := h.service.Vault()
	if store == nil {
		http.Error(w, "Vault 未启用", http.StatusServiceUnavailable)
		return
	}
	r.SetPathValue("session", "private-vault")
	who := identity(r)
	who.Direct = true
	ctx := chat.WithIdentity(r.Context(), who)
	w.Header().Set("Cache-Control", "no-store")
	var out any
	var err error
	switch {
	case r.Method == http.MethodGet:
		out, err = store.List(ctx, r.URL.Query().Get("origin"))
	case r.Method == http.MethodDelete:
		err = store.Remove(ctx, r.PathValue("handle"))
		out = map[string]bool{"removed": err == nil}
	case r.PathValue("source") != "":
		var in struct {
			Password string `json:"password"`
		}
		if decodeAPI(w, r, &in) != nil {
			http.Error(w, "无效解锁请求", http.StatusBadRequest)
			return
		}
		err = store.Unlock(ctx, r.PathValue("source"), in.Password)
		in.Password = ""
		out = map[string]bool{"unlocked": err == nil}
	default:
		var in vault.Item
		if decodeAPI(w, r, &in) != nil {
			http.Error(w, "无效秘密记录", http.StatusBadRequest)
			return
		}
		out, err = store.Put(ctx, in)
		clear(in.Fields)
	}
	if err != nil {
		http.Error(w, "Vault 操作失败（凭据、站点或授权不满足要求）", http.StatusForbidden)
		return
	}
	respond(w, out)
}
