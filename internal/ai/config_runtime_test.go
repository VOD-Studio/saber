package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

func TestService_ChatTaskConfiguration(t *testing.T) {
	for _, api := range []string{"openai-completions", "openai-responses"} {
		t.Run(api, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				key := "max_tokens"
				if api == "openai-responses" {
					key = "max_output_tokens"
				}
				if request[key] != float64(1234) || request["stream"] == true {
					t.Errorf("model budget or non-stream setting lost: %v", request)
				}
				if request["temperature"] != 0.2 {
					t.Errorf("model temperature lost: %v", request)
				}
				http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			cfg := config.DefaultAIConfig()
			cfg.Enabled, cfg.StreamEnabled, cfg.Retry.Enabled = true, false, false
			cfg.DefaultModel = "test.model"
			cfg.Providers = map[string]config.ProviderConfig{"test": {
				Type: "openai", API: api, BaseURL: server.URL, ReasoningEffort: "none",
				Models: map[string]config.ModelConfig{"model": {Model: "model", MaxTokens: 1234, Temperature: 0.2}},
			}}
			s, err := NewService(&cfg, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Stop)
			if err = s.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")); err != nil {
				t.Fatal(err)
			}
			message := chat.Message{Session: chat.Session{Platform: "tui", Account: "local", Conversation: "config"}, ID: "one", SenderID: "local", Text: "hello"}
			task, err := s.SubmitChatTask(context.Background(), message, "", "")
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				current, err := s.tasks.Get(context.Background(), message.Session, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if current.Status == "failed" {
					if attempts.Load() != 1 {
						t.Fatalf("retry disabled but made %d requests", attempts.Load())
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("task did not finish")
		})
	}
}
