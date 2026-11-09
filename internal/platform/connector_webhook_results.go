package platform

import (
	"encoding/json"
	"net/http"
	"strings"

	"rua.plus/saber/internal/chat"
)

type webhookMetadata struct{ Message chat.Message }

func (p *connector) webhookResult(w http.ResponseWriter, r *http.Request) bool {
	if p.name != "webhook" || !strings.HasPrefix(r.URL.Path, "/responses/") {
		return false
	}
	if !equalSecret(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), p.cfg.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	}
	id := strings.TrimPrefix(r.URL.Path, "/responses/")
	if strings.ContainsAny(id, "/\\\x00") {
		http.NotFound(w, r)
		return true
	}
	var metadata webhookMetadata
	if err := json.Unmarshal([]byte(p.state("receipt-message:"+id)), &metadata.Message); err != nil {
		http.NotFound(w, r)
		return true
	}
	if err := p.incomingAllowed(metadata.Message); err != nil {
		http.Error(w, "authorization revoked", http.StatusForbidden)
		return true
	}
	var state, detail string
	if err := p.db.QueryRowContext(r.Context(), "SELECT state,error FROM inbox WHERE id=?", id).Scan(&state, &detail); err != nil {
		http.NotFound(w, r)
		return true
	}
	if r.Method == "DELETE" {
		if state != "dispatched" {
			http.Error(w, "response is not cancelable", http.StatusConflict)
			return true
		}
		if _, err := p.db.ExecContext(r.Context(), "UPDATE inbox SET state='canceled',error='caller explicitly canceled' WHERE id=?", id); err != nil {
			http.Error(w, "cancellation failed", 500)
			return true
		}
		p.cancelMessage(id)
		state = "canceled"
	} else if r.Method != "GET" {
		w.WriteHeader(405)
		return true
	}
	result := map[string]any{"id": id, "status": state}
	if state == "completed" {
		result["text"] = p.state("reply:" + metadata.Message.Session.Conversation + ":" + metadata.Message.ID)
	}
	if detail != "" {
		result["error"] = detail
	}
	writeJSON(w, 200, result)
	return true
}
