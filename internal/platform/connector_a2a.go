package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"rua.plus/saber/internal/chat"
)

type a2aRecord struct {
	ID        string `json:"id"`
	ContextID string `json:"contextId"`
	User      string `json:"user"`
	Created   string `json:"created"`
}

func (p *connector) a2aRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && (r.URL.Path == "/.well-known/agent-card.json" || r.URL.Path == "/.well-known/agent.json") {
		writeJSON(w, 200, map[string]any{"name": "Saber", "description": "Go 原生已授权 Agent", "url": p.cfg.PublicURL + p.cfg.WebhookPath, "version": "1.0.0", "protocolVersion": "0.3.0", "preferredTransport": "JSONRPC", "capabilities": map[string]bool{"streaming": true}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"}, "securitySchemes": map[string]any{"bearer": map[string]string{"type": "http", "scheme": "bearer"}}, "security": []any{map[string]any{"bearer": []string{}}}, "skills": []any{map[string]any{"id": "chat", "name": "Saber 聊天和任务", "description": "遵守服务端授权的聊天和任务执行", "tags": []string{"agent"}}}})
		return
	}
	if r.Method != "POST" || r.URL.Path != p.cfg.WebhookPath {
		http.NotFound(w, r)
		return
	}
	if !equalSecret(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), p.cfg.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var request struct {
		ID      any            `json:"id"`
		JSONRPC string         `json:"jsonrpc"`
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	respond := func(v any) { writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": v}) }
	fail := func(code int, message string) {
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": code, "message": message}})
	}
	peer := p.cfg.Username
	if peer == "" {
		peer = "a2a-client"
	}
	if !p.cfg.AllowAllUsers && !contains(p.cfg.AllowedUsers, peer) {
		fail(-32001, "peer not authorized")
		return
	}
	params := request.Params
	switch request.Method {
	case "message/send", "message/stream":
		message := obj(params["message"])
		if str(message["role"]) != "user" {
			fail(-32602, "message.role must be user")
			return
		}
		id := str(message["messageId"])
		if id == "" {
			fail(-32602, "messageId required for idempotency")
			return
		}
		contextID := str(message["contextId"])
		if contextID == "" {
			contextID = uuid.NewString()
		}
		var text strings.Builder
		var attachments []chat.Attachment
		for _, raw := range arr(message["parts"]) {
			part := obj(raw)
			switch str(part["kind"]) {
			case "text":
				text.WriteString(str(part["text"]))
			case "file":
				file := obj(part["file"])
				encoded := str(file["bytes"])
				if encoded == "" {
					fail(-32602, "file bytes required; arbitrary file URLs are not fetched")
					return
				}
				a, err := decodeA2AFile(encoded, str(file["mimeType"]), str(file["name"]), p.cfg.MaxMediaBytes)
				if err != nil {
					fail(-32602, err.Error())
					return
				}
				attachments = append(attachments, a)
			default:
				fail(-32602, "unsupported message part")
				return
			}
		}
		existing := p.state("a2a:" + id)
		record := a2aRecord{ID: id, ContextID: contextID, User: peer, Created: time.Now().UTC().Format(time.RFC3339)}
		if existing != "" {
			if err := json.Unmarshal([]byte(existing), &record); err != nil {
				fail(-32603, "corrupt task record")
				return
			}
			if record.User != peer {
				fail(-32001, "task access denied")
				return
			}
			contextID = record.ContextID
		} else {
			if err := p.setState(r.Context(), "a2a:"+id, jsonString(record)); err != nil {
				fail(-32603, err.Error())
				return
			}
		}
		msg := chat.Message{ID: id, SenderID: peer, Direct: true, Text: text.String(), Attachments: attachments, Session: chat.Session{Conversation: contextID}, ReplyTo: id}
		if err := p.accept(r.Context(), msg); err != nil {
			fail(-32001, err.Error())
			return
		}
		if request.Method == "message/stream" {
			p.a2aStream(w, r, request.ID, record)
			return
		}
		blocking := true
		if options := obj(params["configuration"]); options["blocking"] != nil {
			blocking, _ = options["blocking"].(bool)
		}
		if blocking {
			waitCtx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
			defer cancel()
			_, err := p.waitReply(waitCtx, id)
			if err != nil && waitCtx.Err() == nil {
				task, _ := p.a2aTask(record)
				respond(task)
				return
			}
		}
		task, err := p.a2aTask(record)
		if err != nil {
			fail(-32603, err.Error())
			return
		}
		respond(task)
	case "tasks/get", "tasks/cancel", "tasks/resubscribe":
		id := str(params["id"])
		var record a2aRecord
		if err := json.Unmarshal([]byte(p.state("a2a:"+id)), &record); err != nil || record.User != peer {
			fail(-32001, "task not found or access denied")
			return
		}
		if request.Method == "tasks/cancel" {
			result, err := p.db.ExecContext(r.Context(), "UPDATE inbox SET state='canceled',error='A2A caller explicitly canceled' WHERE id=? AND state='dispatched'", id)
			if err != nil {
				fail(-32603, err.Error())
				return
			}
			n, _ := result.RowsAffected()
			if n == 0 {
				fail(-32002, "task is not cancelable")
				return
			}
			p.cancelMessage(id)
		}
		if request.Method == "tasks/resubscribe" {
			p.a2aStream(w, r, request.ID, record)
			return
		}
		task, err := p.a2aTask(record)
		if err != nil {
			fail(-32603, err.Error())
			return
		}
		respond(task)
	case "tasks/list":
		rows, err := p.db.QueryContext(r.Context(), "SELECT value FROM state WHERE key LIKE 'a2a:%' ORDER BY rowid DESC LIMIT 100")
		if err != nil {
			fail(-32603, err.Error())
			return
		}
		var records []a2aRecord
		for rows.Next() {
			var value string
			if err = rows.Scan(&value); err != nil {
				_ = rows.Close()
				fail(-32603, err.Error())
				return
			}
			var record a2aRecord
			if json.Unmarshal([]byte(value), &record) == nil && record.User == peer {
				records = append(records, record)
			}
		}
		_ = rows.Close()
		tasks := make([]any, 0, len(records))
		for _, record := range records {
			task, err := p.a2aTask(record)
			if err != nil {
				fail(-32603, err.Error())
				return
			}
			tasks = append(tasks, task)
		}
		respond(map[string]any{"tasks": tasks, "totalSize": len(tasks)})
	default:
		fail(-32601, "method not supported")
	}
}
func (p *connector) a2aTask(record a2aRecord) (map[string]any, error) {
	var state, detail string
	if err := p.db.QueryRow("SELECT state,error FROM inbox WHERE id=?", record.ID).Scan(&state, &detail); err != nil {
		return nil, err
	}
	mapped := map[string]string{"dispatched": "working", "completed": "completed", "failed": "failed", "interrupted": "failed", "canceled": "canceled"}[state]
	task := map[string]any{"kind": "task", "id": record.ID, "contextId": record.ContextID, "status": map[string]any{"state": mapped, "timestamp": time.Now().UTC().Format(time.RFC3339)}}
	if state == "completed" {
		text := p.state("reply:" + record.ContextID + ":" + record.ID)
		task["artifacts"] = []any{map[string]any{"artifactId": record.ID + "-response", "parts": []any{map[string]string{"kind": "text", "text": text}}}}
	} else if detail != "" {
		obj(task["status"])["message"] = map[string]any{"kind": "message", "role": "agent", "messageId": record.ID + "-error", "parts": []any{map[string]string{"kind": "text", "text": detail}}}
	}
	return task, nil
}
func (p *connector) a2aStream(w http.ResponseWriter, r *http.Request, rpcID any, record a2aRecord) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	task, err := p.a2aTask(record)
	if err != nil {
		return
	}
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpcID, "result": task})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		task, err = p.a2aTask(record)
		if err != nil {
			return
		}
		state := str(obj(task["status"])["state"])
		if state != "working" {
			event := map[string]any{"kind": "status-update", "taskId": record.ID, "contextId": record.ContextID, "status": task["status"], "final": true}
			if artifacts := arr(task["artifacts"]); len(artifacts) > 0 {
				artifact := map[string]any{"kind": "artifact-update", "taskId": record.ID, "contextId": record.ContextID, "artifact": artifacts[0], "lastChunk": true}
				data, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpcID, "result": artifact})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			}
			data, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpcID, "result": event})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
func (p *connector) cancelMessage(id string) {
	p.mu.Lock()
	cancel := p.messageCancels[id]
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
