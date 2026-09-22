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

	"rua.plus/saber/internal/agent"
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
				if request["temperature"] != float64(0) {
					t.Errorf("model temperature lost: %v", request)
				}
				http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			cfg := *config.DefaultConfig()
			cfg.AI.Enabled, cfg.Agent.StreamEnabled, cfg.Agent.Retry.Enabled = true, false, false
			cfg.AI.DefaultModel = "test.model"
			cfg.AI.Providers = map[string]config.ProviderConfig{"test": {
				Type: "openai", API: api, BaseURL: server.URL, ReasoningEffort: "none",
				Models: map[string]config.ModelConfig{"model": {Model: "model", MaxTokens: 1234, Temperature: new(float64(0))}},
			}}
			s, err := NewService(&cfg)
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

func TestService_CircuitBreakerAcrossTasks(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	cfg := config.DefaultConfig()
	cfg.AI.Enabled, cfg.AI.DefaultModel = true, "test.model"
	cfg.AI.Providers = map[string]config.ProviderConfig{"test": {Type: "openai", BaseURL: upstream.URL}}
	cfg.Agent.Retry.Enabled = false
	cfg.Agent.CircuitBreaker.Enabled, cfg.Agent.CircuitBreaker.FailureThreshold = true, 1
	s, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	for range 2 {
		_, err := s.RunAgent(context.Background(), agent.Request{Model: "test.model"}, nil)
		if err == nil {
			t.Fatal("expected upstream/circuit error")
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("circuit reset between tasks: %d requests", requests.Load())
	}
}
