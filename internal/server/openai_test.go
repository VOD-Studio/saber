package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/config"
)

// newOpenAITestServer 创建指向假上游且不启用任务数据库的入口。
func newOpenAITestServer(t *testing.T, upstream http.HandlerFunc, mutate func(*config.Config)) *httptest.Server {
	t.Helper()
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: upstreamServer.URL, APIKey: "secret-test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.Retry.MaxRetries = 0
	if mutate != nil {
		mutate(&cfg)
	}
	service, err := ai.NewService(&cfg)
	require.NoError(t, err)
	t.Cleanup(service.Stop)
	endpoint := httptest.NewServer(New(service, "test-token"))
	t.Cleanup(endpoint.Close)
	return endpoint
}

// openAICall 发送一次带令牌的请求。
func openAICall(t *testing.T, endpoint *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, endpoint.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := endpoint.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, response.Body.Close()) })
	return response
}

// decodeOpenAITestError 读取 OpenAI 风格错误。
func decodeOpenAITestError(t *testing.T, response *http.Response) openAIErrorBody {
	t.Helper()
	var body openAIError
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	return body.Error
}

func TestOpenAI_Models(t *testing.T) {
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unused", http.StatusInternalServerError)
	}, func(cfg *config.Config) {
		cfg.AI.Providers["openai"] = config.ProviderConfig{
			Type: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "secret-test",
			Models: map[string]config.ModelConfig{"alpha": {Model: "alpha"}, "zeta": {Model: "zeta"}},
		}
		cfg.AI.DefaultModel = "openai.zeta"
	})
	response := openAICall(t, endpoint, "GET", "/v1/models", "")
	require.Equal(t, http.StatusOK, response.StatusCode)
	var body openAIModelList
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Equal(t, "list", body.Object)
	require.Len(t, body.Data, 2)
	require.Equal(t, "openai.zeta", body.Data[0].ID, "默认模型必须排在首项")
	require.Equal(t, "openai.alpha", body.Data[1].ID)
	for _, model := range body.Data {
		require.Equal(t, "model", model.Object)
		require.Equal(t, "saber", model.OwnedBy)
		require.Positive(t, model.Created)
	}
}

func TestOpenAI_ChatCompletion(t *testing.T) {
	var received map[string]any
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("解析上游请求失败: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprint(w, `{"id":"x","object":"chat.completion","created":1,"model":"local","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
		if err != nil {
			t.Errorf("写出上游响应失败: %v", err)
		}
	}, nil)

	response := openAICall(t, endpoint, "POST", "/v1/chat/completions", `{"model":"openai.local","messages":[{"role":"system","content":"你是助手"},{"role":"user","content":"你好"}],"temperature":0.2,"max_tokens":32}`)
	require.Equal(t, http.StatusOK, response.StatusCode)
	var body openAIChatCompletion
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.True(t, strings.HasPrefix(body.ID, "chatcmpl-"))
	require.Equal(t, "chat.completion", body.Object)
	require.Equal(t, "openai.local", body.Model)
	require.Positive(t, body.Created)
	require.Len(t, body.Choices, 1)
	require.Equal(t, 0, body.Choices[0].Index)
	require.Equal(t, "assistant", body.Choices[0].Message.Role)
	require.Equal(t, "你好", body.Choices[0].Message.Content)
	require.Equal(t, "stop", body.Choices[0].FinishReason)
	require.Equal(t, openAIUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, body.Usage)

	messages, ok := received["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
	first, ok := messages[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "system", first["role"])
	require.Equal(t, "你是助手", first["content"])
	require.Equal(t, 0.2, received["temperature"])
}

func TestOpenAI_ChatCompletionStream(t *testing.T) {
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"，世界\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		} {
			if _, err := io.WriteString(w, chunk); err != nil {
				t.Errorf("写出上游流失败: %v", err)
				return
			}
			flusher.Flush()
		}
	}, nil)

	response := openAICall(t, endpoint, "POST", "/v1/chat/completions", `{"model":"openai.local","messages":[{"role":"user","content":"你好"}],"stream":true}`)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, response.Header.Get("Content-Type"), "text/event-stream")
	data := readOpenAISSE(t, response.Body)
	require.Len(t, data, 5)
	require.Equal(t, "[DONE]", data[len(data)-1])

	var first openAIChunk
	require.NoError(t, json.Unmarshal([]byte(data[0]), &first))
	require.Equal(t, "chat.completion.chunk", first.Object)
	require.Equal(t, "assistant", first.Choices[0].Delta.Role)
	require.Empty(t, first.Choices[0].Delta.Content)
	require.Nil(t, first.Choices[0].FinishReason)

	require.Contains(t, data[1], `"content":"你好"`)
	require.Contains(t, data[2], `"content":"，世界"`)

	var last openAIChunk
	require.NoError(t, json.Unmarshal([]byte(data[3]), &last))
	require.NotNil(t, last.Choices[0].FinishReason)
	require.Equal(t, "stop", *last.Choices[0].FinishReason)
	require.Empty(t, last.Choices[0].Delta)
}

func TestOpenAI_ChatCompletionStreamCancel(t *testing.T) {
	started := make(chan struct{})
	upstreamDone := make(chan struct{})
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if _, err := io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"稍等\"}}]}\n\n"); err != nil {
			t.Errorf("写出上游流失败: %v", err)
			return
		}
		flusher.Flush()
		close(started)
		<-r.Context().Done()
		close(upstreamDone)
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint.URL+"/v1/chat/completions", strings.NewReader(`{"model":"openai.local","messages":[{"role":"user","content":"你好"}],"stream":true}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := endpoint.Client().Do(req)
	require.NoError(t, err)
	reader := bufio.NewReader(response.Body)
	for {
		line, readErr := reader.ReadString('\n')
		require.NoError(t, readErr)
		if strings.HasPrefix(line, "data: ") {
			break
		}
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("上游未收到请求")
	}
	cancel()
	require.NoError(t, response.Body.Close())
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后上游请求未被取消")
	}
}

func TestOpenAI_ChatCompletionErrors(t *testing.T) {
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unused", http.StatusInternalServerError)
	}, nil)
	longContent := strings.Repeat("a", maxOpenAIMessageBytes+1)
	manyMessages := make([]string, 0, maxOpenAIMessages+1)
	for range maxOpenAIMessages + 1 {
		manyMessages = append(manyMessages, `{"role":"user","content":"x"}`)
	}
	for _, tc := range []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{name: "无效 JSON", body: `{"model":`, status: http.StatusBadRequest, code: "invalid_json"},
		{name: "空消息", body: `{"model":"openai.local","messages":[]}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "未知模型", body: `{"model":"openai.missing","messages":[{"role":"user","content":"x"}]}`, status: http.StatusNotFound, code: "model_not_found"},
		{name: "工具参数", body: `{"model":"openai.local","messages":[{"role":"user","content":"x"}],"tools":[]}`, status: http.StatusBadRequest, code: "unsupported_parameter"},
		{name: "工具选择", body: `{"model":"openai.local","messages":[{"role":"user","content":"x"}],"tool_choice":"auto"}`, status: http.StatusBadRequest, code: "unsupported_parameter"},
		{name: "旧函数参数", body: `{"model":"openai.local","messages":[{"role":"user","content":"x"}],"functions":[]}`, status: http.StatusBadRequest, code: "unsupported_parameter"},
		{name: "多个候选", body: `{"model":"openai.local","messages":[{"role":"user","content":"x"}],"n":2}`, status: http.StatusBadRequest, code: "unsupported_parameter"},
		{name: "日志概率", body: `{"model":"openai.local","messages":[{"role":"user","content":"x"}],"logprobs":true}`, status: http.StatusBadRequest, code: "unsupported_parameter"},
		{name: "流式选项", body: `{"model":"openai.local","messages":[{"role":"user","content":"x"}],"stream_options":{"include_usage":true}}`, status: http.StatusBadRequest, code: "unsupported_parameter"},
		{name: "工具角色", body: `{"model":"openai.local","messages":[{"role":"tool","content":"x"}]}`, status: http.StatusBadRequest, code: "unsupported_role"},
		{name: "图片内容", body: `{"model":"openai.local","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`, status: http.StatusBadRequest, code: "unsupported_content"},
		{name: "过多消息", body: `{"model":"openai.local","messages":[` + strings.Join(manyMessages, ",") + `]}`, status: http.StatusBadRequest, code: "too_many_messages"},
		{name: "单条过长", body: `{"model":"openai.local","messages":[{"role":"user","content":"` + longContent + `"}]}`, status: http.StatusBadRequest, code: "message_too_large"},
		{name: "温度越界", body: `{"model":"openai.local","messages":[{"role":"user","content":"x"}],"temperature":3}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "输出上限非法", body: `{"model":"openai.local","messages":[{"role":"user","content":"x"}],"max_tokens":0}`, status: http.StatusBadRequest, code: "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := openAICall(t, endpoint, "POST", "/v1/chat/completions", tc.body)
			require.Equal(t, tc.status, response.StatusCode)
			require.Equal(t, tc.code, decodeOpenAITestError(t, response).Code)
		})
	}
}

func TestOpenAI_RequestTooLarge(t *testing.T) {
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unused", http.StatusInternalServerError)
	}, nil)
	body := `{"model":"openai.local","messages":[{"role":"user","content":"` + strings.Repeat("a", maxOpenAIBodyBytes) + `"}]}`
	response := openAICall(t, endpoint, "POST", "/v1/chat/completions", body)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
	require.Equal(t, "request_too_large", decodeOpenAITestError(t, response).Code)
}

func TestOpenAI_AuthAndOrigin(t *testing.T) {
	endpoint := newOpenAITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unused", http.StatusInternalServerError)
	}, nil)
	req, err := http.NewRequest("GET", endpoint.URL+"/v1/models", nil)
	require.NoError(t, err)
	response, err := endpoint.Client().Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)

	req, err = http.NewRequest("GET", endpoint.URL+"/v1/models", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Origin", "https://example.com")
	response, err = endpoint.Client().Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
}

// readOpenAISSE 读取 SSE 的 data 负载直到 [DONE]。
func readOpenAISSE(t *testing.T, body io.Reader) []string {
	t.Helper()
	var data []string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		data = append(data, payload)
		if payload == "[DONE]" {
			break
		}
	}
	require.NoError(t, scanner.Err())
	return data
}
