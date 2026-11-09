package server

import (
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIIdempotencyDoesNotDispatchSecondModelRequest(t *testing.T) {
	var calls atomic.Int32
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}, nil)
	body := `{"session":"idem","id":"same","model":"openai.local","messages":[{"role":"user","content":"perform exactly once"}]}`
	first := openAICall(t, endpoint, "POST", "/v1/chat/completions", body)
	require.Equal(t, http.StatusOK, first.StatusCode)
	var a map[string]any
	require.NoError(t, json.NewDecoder(first.Body).Decode(&a))
	second := openAICall(t, endpoint, "POST", "/v1/chat/completions", body)
	require.Equal(t, http.StatusOK, second.StatusCode)
	var b map[string]any
	require.NoError(t, json.NewDecoder(second.Body).Decode(&b))
	require.Equal(t, a["task_id"], b["task_id"])
	require.Equal(t, int32(1), calls.Load())
	changed := openAICall(t, endpoint, "POST", "/v1/chat/completions", `{"session":"idem","id":"same","model":"openai.local","messages":[{"role":"user","content":"different side effect"}]}`)
	require.Equal(t, http.StatusConflict, changed.StatusCode)
	require.Equal(t, int32(1), calls.Load())
}
func TestOpenAIProviderFailureIsNotSuccessfulStop(t *testing.T) {
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	}, nil)
	response := openAICall(t, endpoint, "POST", "/v1/chat/completions", `{"model":"openai.local","messages":[{"role":"user","content":"do work"}]}`)
	var body struct {
		Choices   []openAIChoice   `json:"choices"`
		Error     *openAIErrorBody `json:"error"`
		RunStatus string           `json:"run_status"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Len(t, body.Choices, 1)
	require.Equal(t, "error", body.Choices[0].FinishReason)
	require.NotNil(t, body.Error)
	require.Equal(t, "failed", body.RunStatus)
}
