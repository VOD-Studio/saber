package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (p *connector) startRaftBridge(ctx context.Context) error {
	if p.cfg.Profile == "" || p.cfg.Token == "" {
		return errors.New("raft 需要已登录 profile 和私有 token")
	}
	endpoint := p.cfg.PublicURL
	if endpoint == "" {
		endpoint = "http://" + p.cfg.Listen
	}
	command := exec.CommandContext(ctx, p.cfg.Command, "--profile", p.cfg.Profile, "agent", "bridge", "--wake-adapter", "wake-channel", "--wake-channel-endpoint", strings.TrimRight(endpoint, "/")+p.cfg.WebhookPath)
	command.Env = append(os.Environ(), "RAFT_PROFILE="+p.cfg.Profile, "RAFT_CHANNEL_TOKEN="+p.cfg.Token)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return err
	}
	go func() {
		if err := command.Wait(); err != nil && ctx.Err() == nil {
			slog.Error("Raft bridge 退出", "error", err)
		}
	}()
	return nil
}
func (p *connector) raftAuxiliary(w http.ResponseWriter, r *http.Request) bool {
	if p.name != "raft" {
		return false
	}
	if r.URL.Path == "/health" {
		writeJSON(w, 200, map[string]any{"status": "ok", "platform": "raft", "runtimeSession": p.cfg.Profile, "activity": map[string]any{"endpoint": "/activity", "drainEndpoint": "/activity/drain"}})
		return true
	}
	if r.URL.Path != "/activity" && r.URL.Path != "/activity/drain" {
		return false
	}
	if !equalSecret(r.Header.Get("x-raft-bridge-token"), p.cfg.Token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return true
	}
	if r.URL.Path == "/activity" && r.Method == "POST" {
		var event map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&event); err != nil {
			http.Error(w, "invalid activity", 400)
			return true
		}
		if _, err := p.db.ExecContext(r.Context(), "CREATE TABLE IF NOT EXISTS raft_activity(id TEXT PRIMARY KEY,body TEXT NOT NULL,created INTEGER NOT NULL)"); err != nil {
			http.Error(w, "activity store failed", 500)
			return true
		}
		if _, err := p.db.ExecContext(r.Context(), "INSERT INTO raft_activity(id,body,created) VALUES(?,?,?)", uuid.NewString(), jsonString(event), time.Now().UnixMilli()); err != nil {
			http.Error(w, "activity store failed", 500)
			return true
		}
		writeJSON(w, 202, map[string]bool{"ok": true})
		return true
	}
	if r.URL.Path == "/activity/drain" && r.Method == "GET" {
		if _, err := p.db.ExecContext(r.Context(), "CREATE TABLE IF NOT EXISTS raft_activity(id TEXT PRIMARY KEY,body TEXT NOT NULL,created INTEGER NOT NULL)"); err != nil {
			http.Error(w, "activity store failed", 500)
			return true
		}
		tx, err := p.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "activity store failed", 500)
			return true
		}
		defer func() { _ = tx.Rollback() }()
		rows, err := tx.QueryContext(r.Context(), "SELECT id,body FROM raft_activity ORDER BY created,id LIMIT 200")
		if err != nil {
			http.Error(w, "activity query failed", 500)
			return true
		}
		var ids []string
		var events []any
		for rows.Next() {
			var id, body string
			if err = rows.Scan(&id, &body); err != nil {
				_ = rows.Close()
				http.Error(w, "activity query failed", 500)
				return true
			}
			var event any
			if err = json.Unmarshal([]byte(body), &event); err != nil {
				_ = rows.Close()
				http.Error(w, "corrupt activity", 500)
				return true
			}
			ids = append(ids, id)
			events = append(events, event)
		}
		_ = rows.Close()
		for _, id := range ids {
			if _, err = tx.ExecContext(r.Context(), "DELETE FROM raft_activity WHERE id=?", id); err != nil {
				http.Error(w, "activity delete failed", 500)
				return true
			}
		}
		if err = tx.Commit(); err != nil {
			http.Error(w, "activity commit failed", 500)
			return true
		}
		writeJSON(w, 200, map[string]any{"schema": "raft-activity-drain.v1", "events": events, "drained": len(events)})
		return true
	}
	http.Error(w, fmt.Sprintf("%s method not supported", r.URL.Path), http.StatusMethodNotAllowed)
	return true
}
