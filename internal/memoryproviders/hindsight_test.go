package memoryproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func recallTestIdentity(sender, conversation string, direct bool) context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: chat.Session{Platform: "test", Account: "bot", Conversation: conversation}, SenderID: sender, Direct: direct})
}

func recallTestCall(t *testing.T, b Backend, ctx context.Context, operation string, payload map[string]any) map[string]any {
	t.Helper()
	value, err := b.Call(ctx, operation, map[string]any{"payload": payload, "identity": "attacker", "scope": "attacker", "conversation_key": "attacker"})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("unexpected %s result: %T", operation, value)
	}
	return result
}

func recallTestReply(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode protocol response: %v", err)
	}
}

func recallTestDecode(t *testing.T, w http.ResponseWriter, r *http.Request, value any) bool {
	t.Helper()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		t.Errorf("invalid %s %s protocol body: %v", r.Method, r.URL.Path, err)
		http.Error(w, "invalid request schema", 400)
		return false
	}
	return true
}

var recallTestPreference = regexp.MustCompile(`(?i)prefer (tea|coffee)`)

func recallTestFacts(documents map[string]string) []any {
	seen := map[string]bool{}
	facts := []any{}
	for _, content := range documents {
		for _, match := range recallTestPreference.FindAllStringSubmatch(content, -1) {
			text := "Preferred beverage is " + strings.ToLower(match[1]) + "."
			if !seen[text] {
				seen[text] = true
				facts = append(facts, map[string]any{"id": "memory-" + strings.ToLower(match[1]), "text": text, "memory": text, "type": "observation"})
			}
		}
	}
	return facts
}

func TestHindsightProtocolLifecycleAndIsolation(t *testing.T) {
	var mu sync.Mutex
	documents := map[string]map[string]string{}
	writes := 0
	exposeText := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer original-key" {
			t.Errorf("credential snapshot mismatch: %q", r.Header.Get("Authorization"))
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("User-Agent") != "saber-memory-hindsight" {
			t.Error("incorrect harness source")
		}
		if !strings.HasPrefix(r.URL.Path, "/gateway/v1/default/banks/") {
			t.Errorf("incorrect Hindsight endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/gateway/v1/default/banks/"), "/", 2)
		scope := parts[0]
		suffix := ""
		if len(parts) == 2 {
			suffix = "/" + parts[1]
		}
		if !strings.HasPrefix(scope, "saber-") || len(scope) != 70 {
			t.Errorf("untrusted bank %q", scope)
			http.Error(w, "scope", 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && suffix == "/config":
			if documents[scope] == nil {
				http.Error(w, "not found", 404)
				return
			}
			recallTestReply(t, w, map[string]any{"bank_id": scope, "config": map[string]any{}, "overrides": map[string]any{}})
		case r.Method == http.MethodPut && suffix == "":
			var body struct{}
			if !recallTestDecode(t, w, r, &body) {
				return
			}
			if documents[scope] == nil {
				documents[scope] = map[string]string{}
			}
			recallTestReply(t, w, map[string]any{"bank_id": scope, "name": scope, "disposition": map[string]any{}, "mission": ""})
		case r.Method == http.MethodPost && suffix == "/memories":
			var body struct {
				Items []struct {
					Content    string            `json:"content"`
					DocumentID string            `json:"document_id"`
					Context    string            `json:"context"`
					Metadata   map[string]string `json:"metadata"`
					Tags       []string          `json:"tags"`
					Timestamp  string            `json:"timestamp"`
				} `json:"items"`
				Async bool `json:"async"`
			}
			if !recallTestDecode(t, w, r, &body) {
				return
			}
			if body.Async || len(body.Items) != 1 {
				t.Error("retain must synchronously process one source document")
				http.Error(w, "schema", 400)
				return
			}
			item := body.Items[0]
			if item.Metadata["scope"] != scope || item.Metadata["source"] != "saber" {
				t.Error("missing trusted provenance")
			}
			if documents[scope] == nil {
				documents[scope] = map[string]string{}
			}
			documents[scope][item.DocumentID] = item.Content
			writes++
			recallTestReply(t, w, map[string]any{"success": true, "bank_id": scope, "items_count": 1, "async": false})
		case r.Method == http.MethodPost && (suffix == "/memories/recall" || suffix == "/reflect"):
			var body struct {
				Query     string   `json:"query"`
				Budget    string   `json:"budget"`
				MaxTokens int      `json:"max_tokens"`
				Types     []string `json:"types"`
				Tags      []string `json:"tags"`
				TagsMatch string   `json:"tags_match"`
			}
			if !recallTestDecode(t, w, r, &body) {
				return
			}
			if body.Query == "" || body.Budget != "mid" || body.MaxTokens != 4096 {
				t.Error("invalid recall budget or query")
			}
			facts := recallTestFacts(documents[scope])
			if suffix == "/reflect" {
				text := ""
				if len(facts) > 0 {
					text = "According to the bank, " + facts[0].(map[string]any)["text"].(string)
				}
				recallTestReply(t, w, map[string]any{"text": text})
			} else {
				if len(body.Types) != 1 || body.Types[0] != "observation" {
					t.Error("Hermes observation recall default not applied")
				}
				recallTestReply(t, w, map[string]any{"results": facts})
			}
		case r.Method == http.MethodGet && strings.HasPrefix(suffix, "/documents/"):
			doc := strings.TrimPrefix(suffix, "/documents/")
			content, ok := documents[scope][doc]
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			var original any = content
			if !exposeText {
				original = nil
			}
			recallTestReply(t, w, map[string]any{"id": doc, "bank_id": scope, "original_text": original})
		default:
			t.Errorf("unsupported test protocol route %s %s", r.Method, suffix)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("HINDSIGHT_TEST_KEY", "original-key")
	backend, err := NewHindsight(config.MemoryBackendConfig{Endpoint: server.URL + "/gateway", APIKeyEnv: "HINDSIGHT_TEST_KEY", AllowPrivate: true}, toolkit.Host{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	}()
	t.Setenv("HINDSIGHT_TEST_KEY", "changed-key")
	alice := recallTestIdentity("alice", "dm-a", true)
	bob := recallTestIdentity("bob", "dm-b", true)
	recallTestCall(t, backend, alice, "initialize", nil)
	recallTestCall(t, backend, alice, "turn_start", map[string]any{"message": "preference"})
	messages := []any{map[string]any{"role": "user", "content": "I prefer tea"}, map[string]any{"role": "assistant", "content": "I will remember that."}}
	captured := recallTestCall(t, backend, alice, "sync_turn", map[string]any{"messages": messages})
	if captured["captured"] != true {
		t.Fatal("completed conversation was not retained")
	}
	recallTestCall(t, backend, alice, "sync_turn", map[string]any{"messages": messages})
	mu.Lock()
	count := writes
	mu.Unlock()
	if count != 1 {
		t.Fatalf("unchanged turn wrote %d times", count)
	}
	recallTestCall(t, backend, alice, "sync_turn", map[string]any{"user_content": "Project gamma", "assistant_content": "Understood."})
	scope, _ := ScopedIdentity(alice)
	mu.Lock()
	deltaContent := documents[scope][nativeRecallText(captured, "document_id")]
	mu.Unlock()
	if !strings.Contains(deltaContent, "I prefer tea") || !strings.Contains(deltaContent, "Project gamma") {
		t.Fatal("delta sync replaced earlier conversation turns")
	}
	contextResult := recallTestCall(t, backend, alice, "context", map[string]any{"query": "preferred beverage"})
	if !strings.Contains(nativeRecallText(contextResult, "context"), "Preferred beverage is tea.") {
		t.Fatal("recall did not derive the previously retained preference")
	}
	for _, ctx := range []context.Context{bob, recallTestIdentity("alice", "group", false)} {
		result := recallTestCall(t, backend, ctx, "prefetch", map[string]any{"query": "preferred beverage"})
		if nativeRecallText(result, "context") != "" {
			t.Fatal("memory escaped personal/group scope")
		}
	}
	group := recallTestIdentity("alice", "group", false)
	recallTestCall(t, backend, group, "memory_write", map[string]any{"action": "add", "entry_id": 1, "content": "I prefer coffee"})
	groupResult := recallTestCall(t, backend, recallTestIdentity("bob", "group", false), "context", map[string]any{"query": "preferred beverage"})
	if !strings.Contains(nativeRecallText(groupResult, "context"), "Preferred beverage is coffee.") {
		t.Fatal("trusted shared group bank was incorrectly sender-scoped")
	}
	reflected := recallTestCall(t, backend, alice, "tool", map[string]any{"tool_name": "hindsight_reflect", "arguments": map[string]any{"query": "preferred beverage"}})
	if !strings.Contains(nativeRecallText(reflected, "text"), "According to the bank") {
		t.Fatal("reflect was not routed to the synthesis endpoint")
	}
	if _, err := backend.Call(alice, "tool", map[string]any{"tool_name": "hindsight_recall", "arguments": map[string]any{"query": "tea", "bank_id": "other"}}); err == nil {
		t.Fatal("tool bank override was accepted")
	}
	checkpoint := recallTestCall(t, backend, alice, "pre_compress", map[string]any{"messages": messages})
	if checkpoint["checkpointed"] != true || nativeRecallText(checkpoint, "checkpoint_id") == "" {
		t.Fatal("no readable durable checkpoint acknowledgement")
	}
	mu.Lock()
	exposeText = false
	mu.Unlock()
	if _, err := backend.Call(alice, "pre_compress", map[string]any{"messages": messages}); err == nil {
		t.Fatal("checkpoint accepted when original text storage is disabled")
	}
	if _, err := backend.Call(alice, "delegation", nil); err == nil {
		t.Fatal("unsupported vendor lifecycle fabricated success")
	}
	if _, err := backend.Call(alice, "memory_write", map[string]any{"action": "remove", "content": "tea"}); err == nil {
		t.Fatal("unsupported remove fabricated success")
	}
	recallTestCall(t, backend, alice, "session_end", nil)
}

func TestHindsightErrorCancellationAndClose(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint("close=", shutdown), func(t *testing.T) {
			started := make(chan struct{})
			cancelled := make(chan struct{})
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(cancelled)
			}))
			defer server.Close()
			backend, err := NewHindsight(config.MemoryBackendConfig{Endpoint: server.URL, AllowPrivate: true}, toolkit.Host{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(recallTestIdentity("alice", "dm", true))
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := backend.Call(ctx, "tool", map[string]any{"tool_name": "hindsight_reflect", "arguments": map[string]any{"query": "tea"}})
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP request did not start")
			}
			if shutdown {
				if err := backend.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation was not preserved: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled request did not return")
			}
			select {
			case <-cancelled:
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not reach server")
			}
			if calls.Load() != 1 {
				t.Fatal("cancelled operation was replayed")
			}
			if err := backend.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := backend.Call(recallTestIdentity("alice", "dm", true), "tools", nil); err == nil {
				t.Fatal("closed backend accepted a call")
			}
		})
	}
	for _, status := range []int{401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"secret-api-key"}`))
			}))
			defer server.Close()
			backend, err := NewHindsight(config.MemoryBackendConfig{Endpoint: server.URL, APIKey: "secret-api-key", AllowPrivate: true}, toolkit.Host{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = backend.Close() }()
			_, err = backend.Call(recallTestIdentity("alice", "dm", true), "initialize", nil)
			if err == nil || strings.Contains(err.Error(), "secret-api-key") {
				t.Fatalf("unsafe HTTP error: %v", err)
			}
			if count.Load() != 1 {
				t.Fatal("side effect error was automatically replayed")
			}
		})
	}
}

func TestHindsightConfigurationAndBounds(t *testing.T) {
	t.Setenv("HINDSIGHT_API_KEY", "")
	if _, err := NewHindsight(config.MemoryBackendConfig{}, toolkit.Host{}); err == nil {
		t.Fatal("cloud credential missing was not diagnosed")
	}
	for _, options := range []map[string]any{{"recall_types": []string{"invalid"}}, {"recall_budget": "unlimited"}, {"recall_max_tokens": 0}, {"bank_id": "shared"}, {"auto_recall": "true"}} {
		if _, err := NewHindsight(config.MemoryBackendConfig{Endpoint: "https://example.com", Options: options}, toolkit.Host{}); err == nil {
			t.Fatalf("invalid options accepted: %v", options)
		}
	}
	backend, err := NewHindsight(config.MemoryBackendConfig{Endpoint: "https://example.com"}, toolkit.Host{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	if _, err := backend.Call(context.Background(), "tools", nil); err == nil {
		t.Fatal("missing trusted identity accepted")
	}
	ctx, cancel := context.WithCancel(recallTestIdentity("alice", "dm", true))
	cancel()
	if _, err := backend.Call(ctx, "initialize", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled operation: %v", err)
	}
	if _, err := backend.Call(recallTestIdentity("alice", "dm", true), "tool", map[string]any{"tool_name": "hindsight_retain", "arguments": map[string]any{"content": strings.Repeat("x", 512*1024+1)}}); err == nil {
		t.Fatal("unbounded content accepted")
	}
	if text := nativeRecallBound("界界界", 7); text != "界界" {
		t.Fatalf("UTF-8 output boundary damaged: %q", text)
	}
}
