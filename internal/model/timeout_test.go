package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rua.plus/saber/internal/config"
)

func TestClient_RequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	cfg := config.DefaultAIConfig()
	cfg.TimeoutSeconds = 1
	cfg.Providers = map[string]config.ProviderConfig{"test": {Type: "openai", API: "openai-responses", BaseURL: server.URL}}
	mc, _ := cfg.GetModelConfig("test.model")
	client, err := NewClientWithModel(&mc)
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient.Timeout != time.Second {
		t.Fatalf("timeout did not inherit: %s", client.httpClient.Timeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err = client.CreateChatCompletion(ctx, ChatCompletionRequest{Model: "test.model", Stream: true})
	if err == nil || time.Since(start) > 2*time.Second || ctx.Err() != nil {
		t.Fatalf("stream did not obey request deadline: %v (%s)", err, time.Since(start))
	}
}
