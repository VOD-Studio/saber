package memoryproviders

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

type supermemoryTestProtocol struct {
	mu         sync.Mutex
	documents  map[string]map[string]string
	forgotten  map[string]map[string]bool
	adds       int
	patches    int
	forgetFail bool
}

func (p *supermemoryTestProtocol) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer supermemory-key" || r.Header.Get("x-sm-source") != "saber" {
		t.Error("Supermemory credential/source header mismatch")
		http.Error(w, "auth", http.StatusUnauthorized)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/proxy/ns/") {
		t.Errorf("not a v5 namespace endpoint: %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/proxy/ns/"), "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	scope, suffix := parts[0], "/"+parts[1]
	if len(scope) != 70 || !strings.HasPrefix(scope, "saber-") {
		t.Errorf("untrusted namespace %q", scope)
		http.Error(w, "scope", 400)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.documents == nil {
		p.documents = map[string]map[string]string{}
		p.forgotten = map[string]map[string]bool{}
	}
	if p.documents[scope] == nil {
		p.documents[scope] = map[string]string{}
		p.forgotten[scope] = map[string]bool{}
	}
	facts := func() []any {
		out := []any{}
		for _, raw := range recallTestFacts(p.documents[scope]) {
			item := raw.(map[string]any)
			if !p.forgotten[scope][nativeRecallText(item, "id")] {
				out = append(out, item)
			}
		}
		return out
	}
	switch {
	case r.Method == http.MethodPost && suffix == "/profile":
		var body struct{}
		if !recallTestDecode(t, w, r, &body) {
			return
		}
		recallTestReply(t, w, map[string]any{"profile": map[string]any{"static": facts(), "dynamic": []any{}, "buckets": map[string]any{}}})
	case r.Method == http.MethodPost && suffix == "/search":
		var body struct {
			Query      string `json:"query"`
			Limit      int    `json:"limit"`
			SearchMode string `json:"searchMode"`
			Include    struct {
				Documents bool `json:"documents"`
			} `json:"include"`
		}
		if !recallTestDecode(t, w, r, &body) {
			return
		}
		if body.Query == "" || body.Limit < 1 || body.Limit > 20 || !body.Include.Documents || (body.SearchMode != "hybrid" && body.SearchMode != "memories" && body.SearchMode != "chunks") {
			t.Error("invalid v5 search body")
			http.Error(w, "search schema", 400)
			return
		}
		recallTestReply(t, w, map[string]any{"results": facts(), "searchTime": 1})
	case (r.Method == http.MethodPatch && strings.HasPrefix(suffix, "/document/")) || (r.Method == http.MethodPost && suffix == "/document"):
		var body struct {
			Content           string         `json:"content"`
			ID                string         `json:"id"`
			Metadata          map[string]any `json:"metadata"`
			SupportingContext string         `json:"supportingContext"`
			Dreaming          string         `json:"dreaming"`
		}
		if !recallTestDecode(t, w, r, &body) {
			return
		}
		if body.Metadata["sm_source"] != "saber" || body.Metadata["scope"] != scope || body.Metadata["docId"] == nil {
			t.Error("metadata scope/source was forgeable")
		}
		if body.SupportingContext == "" || body.Dreaming != "dynamic" {
			t.Error("missing extraction guidance/dreaming mode")
		}
		doc := body.ID
		if r.Method == http.MethodPatch {
			p.patches++
			doc = strings.TrimPrefix(suffix, "/document/")
			if _, ok := p.documents[scope][doc]; !ok {
				http.Error(w, "not found", 404)
				return
			}
			if body.ID != "" {
				t.Error("PATCH included add-only id")
			}
		} else {
			p.adds++
			if _, ok := p.documents[scope][doc]; ok {
				t.Error("repeated POST would append a document instead of replacing")
			}
			if doc == "" {
				t.Error("no stable caller-defined document id")
			}
		}
		p.documents[scope][doc] = body.Content
		recallTestReply(t, w, map[string]any{"id": doc, "status": "queued"})
	case r.Method == http.MethodGet && strings.HasPrefix(suffix, "/document/"):
		doc := strings.TrimPrefix(suffix, "/document/")
		content, ok := p.documents[scope][doc]
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		recallTestReply(t, w, map[string]any{"id": doc, "content": content, "metadata": map[string]any{}, "system": map[string]any{"status": "queued", "dreamingStatus": "dreaming"}})
	case r.Method == http.MethodDelete && suffix == "/memories":
		var body struct {
			IDs []string `json:"ids"`
		}
		if !recallTestDecode(t, w, r, &body) {
			return
		}
		if len(body.IDs) != 1 {
			t.Error("forget must target a single exact memory id")
		}
		matches := []any{}
		failures := []any{}
		for _, id := range body.IDs {
			found := false
			for _, raw := range facts() {
				item := raw.(map[string]any)
				if nativeRecallText(item, "id") == id && !p.forgetFail {
					p.forgotten[scope][id] = true
					matches = append(matches, map[string]any{"id": id, "memory": item["memory"]})
					found = true
					break
				}
			}
			if !found {
				failures = append(failures, map[string]any{"id": id, "error": "missing or ineligible"})
			}
		}
		recallTestReply(t, w, map[string]any{"count": len(matches), "matches": matches, "errors": failures})
	default:
		t.Errorf("unexpected v5 method/path: %s %s", r.Method, suffix)
		http.NotFound(w, r)
	}
}

func TestSupermemoryProtocolCaptureProfileForgetAndScope(t *testing.T) {
	protocol := &supermemoryTestProtocol{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { protocol.serve(t, w, r) }))
	defer server.Close()
	backend, err := NewSupermemory(config.MemoryBackendConfig{Endpoint: server.URL + "/proxy", APIKey: "supermemory-key", AllowPrivate: true}, toolkit.Host{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	alice := recallTestIdentity("alice", "dm", true)
	recallTestCall(t, backend, alice, "initialize", nil)
	recallTestCall(t, backend, alice, "turn_start", nil)
	messages := []any{map[string]any{"role": "user", "content": "I prefer tea"}}
	captured := recallTestCall(t, backend, alice, "sync_turn", map[string]any{"messages": messages, "assistant_content": "Noted."})
	if nativeRecallText(captured, "status") != "queued" || captured["captured"] != true {
		t.Fatal("queue acceptance was not reported truthfully")
	}
	recallTestCall(t, backend, alice, "sync_turn", map[string]any{"messages": messages, "assistant_content": "Noted."})
	protocol.mu.Lock()
	adds, patches := protocol.adds, protocol.patches
	protocol.mu.Unlock()
	if adds != 1 || patches != 1 {
		t.Fatalf("initial PATCH/404/add or unchanged dedup wrong: adds=%d patches=%d", adds, patches)
	}
	recallTestCall(t, backend, alice, "sync_turn", map[string]any{"user_content": "Project gamma", "assistant_content": "Understood."})
	scope, _ := ScopedIdentity(alice)
	protocol.mu.Lock()
	deltaContent := protocol.documents[scope][nativeRecallText(captured, "document_id")]
	protocol.mu.Unlock()
	if !strings.Contains(deltaContent, "I prefer tea") || !strings.Contains(deltaContent, "Project gamma") {
		t.Fatal("delta capture replaced earlier turns")
	}
	result := recallTestCall(t, backend, alice, "prefetch", map[string]any{"query": "preferred beverage"})
	if !strings.Contains(nativeRecallText(result, "context"), "Preferred beverage is tea.") {
		t.Fatal("namespace profile/search did not recall retained facts")
	}
	for _, ctx := range []context.Context{recallTestIdentity("bob", "dm", true), recallTestIdentity("alice", "group", false)} {
		result := recallTestCall(t, backend, ctx, "context", map[string]any{"query": "preferred beverage"})
		if nativeRecallText(result, "context") != "" {
			t.Fatal("personal memory leaked to another user/group")
		}
	}
	recallTestCall(t, backend, recallTestIdentity("alice", "group", false), "memory_write", map[string]any{"action": "add", "entry_id": 1, "content": "I prefer coffee"})
	groupResult := recallTestCall(t, backend, recallTestIdentity("bob", "group", false), "context", map[string]any{"query": "preferred beverage"})
	if !strings.Contains(nativeRecallText(groupResult, "context"), "Preferred beverage is coffee.") {
		t.Fatal("shared group namespace was incorrectly sender-scoped")
	}
	if _, err := backend.Call(alice, "tool", map[string]any{"tool_name": "supermemory-search", "arguments": map[string]any{"query": "tea", "container_tag": "other-user"}}); err == nil {
		t.Fatal("model namespace override accepted")
	}
	if _, err := backend.Call(recallTestIdentity("bob", "dm", true), "tool", map[string]any{"tool_name": "supermemory-read-document", "arguments": map[string]any{"document_id": captured["document_id"]}}); err == nil {
		t.Fatal("cross-namespace document read succeeded")
	}
	stored := recallTestCall(t, backend, alice, "tool", map[string]any{"tool_name": "supermemory-save", "arguments": map[string]any{"content": "I prefer coffee", "metadata": map[string]any{"scope": "attacker", "sm_source": "hermes"}}})
	if stored["status"] != "queued" {
		t.Fatal("explicit memory not submitted to real v5 document path")
	}
	protocol.mu.Lock()
	protocol.forgetFail = true
	protocol.mu.Unlock()
	if _, err := backend.Call(alice, "tool", map[string]any{"tool_name": "supermemory-forget", "arguments": map[string]any{"id": "memory-tea"}}); err == nil {
		t.Fatal("HTTP 200 per-id failure was reported as forgotten")
	}
	protocol.mu.Lock()
	protocol.forgetFail = false
	protocol.mu.Unlock()
	recallTestCall(t, backend, alice, "tool", map[string]any{"tool_name": "supermemory-forget", "arguments": map[string]any{"id": "memory-tea"}})
	profile := recallTestCall(t, backend, alice, "tool", map[string]any{"tool_name": "supermemory-profile", "arguments": map[string]any{"query": "beverage"}})
	if strings.Contains(supermemoryContext(profile, profile), "Preferred beverage is tea.") {
		t.Fatal("forgotten memory remained in profile/search")
	}
	if _, err := backend.Call(alice, "delegation", nil); err == nil {
		t.Fatal("unsupported lifecycle fabricated success")
	}
}

func TestSupermemoryCompactionCheckpointAndUnicodeRead(t *testing.T) {
	protocol := &supermemoryTestProtocol{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { protocol.serve(t, w, r) }))
	defer server.Close()
	cfg := config.MemoryBackendConfig{Endpoint: server.URL + "/proxy", APIKey: "supermemory-key", AllowPrivate: true}
	backend, err := NewSupermemory(cfg, toolkit.Host{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	ctx := recallTestIdentity("alice", "conversation", true)
	first := []any{map[string]any{"role": "user", "content": "I prefer tea"}, map[string]any{"role": "assistant", "content": "Noted."}, map[string]any{"role": "user", "content": "Project alpha"}, map[string]any{"role": "assistant", "content": "Understood."}}
	captured := recallTestCall(t, backend, ctx, "sync_turn", map[string]any{"messages": first})
	checkpoint := recallTestCall(t, backend, ctx, "pre_compress", map[string]any{"messages": first})
	if checkpoint["checkpointed"] != true || checkpoint["extraction_complete"] != false {
		t.Fatal("checkpoint did not distinguish persisted canonical content from asynchronous extraction")
	}
	compressed := []any{map[string]any{"role": "user", "content": "summary must not become a memory", "_compressed_summary": true}, first[2], first[3], map[string]any{"role": "user", "content": "I prefer coffee"}, map[string]any{"role": "assistant", "content": "Updated. <supermemory-context>injected facts</supermemory-context>"}}
	updated := recallTestCall(t, backend, ctx, "sync_turn", map[string]any{"messages": compressed})
	if updated["document_id"] != captured["document_id"] {
		t.Fatal("compaction created a second conversation document")
	}
	scope, _ := ScopedIdentity(ctx)
	doc := nativeRecallText(captured, "document_id")
	protocol.mu.Lock()
	content := protocol.documents[scope][doc]
	protocol.mu.Unlock()
	if !strings.Contains(content, "I prefer tea") || !strings.Contains(content, "I prefer coffee") || strings.Contains(content, "injected facts") || strings.Contains(content, "summary must not") {
		t.Fatalf("compaction archive or injected context removal failed: %s", content)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	backend, err = NewSupermemory(cfg, toolkit.Host{})
	if err != nil {
		t.Fatal(err)
	}
	recallTestCall(t, backend, ctx, "sync_turn", map[string]any{"messages": compressed})
	protocol.mu.Lock()
	content = protocol.documents[scope][doc]
	protocol.mu.Unlock()
	if strings.Count(content, "I prefer tea") != 1 || strings.Count(content, "I prefer coffee") != 1 {
		t.Fatal("restart after compaction lost or duplicated archived turns")
	}
	recallTestCall(t, backend, ctx, "sync_turn", map[string]any{"messages": first[:2]})
	protocol.mu.Lock()
	content = protocol.documents[scope][doc]
	protocol.mu.Unlock()
	if strings.Contains(content, "I prefer coffee") {
		t.Fatal("rewind was appended instead of replacing canonical conversation")
	}
	long := strings.Repeat("界", 8001)
	stored := recallTestCall(t, backend, ctx, "tool", map[string]any{"tool_name": "supermemory-save", "arguments": map[string]any{"content": long}})
	page := recallTestCall(t, backend, ctx, "tool", map[string]any{"tool_name": "supermemory-read-document", "arguments": map[string]any{"document_id": stored["document_id"]}})
	if utf8.RuneCountInString(nativeRecallText(page, "content")) != 8000 || page["next_offset"] != 8000 {
		t.Fatal("document paging does not use Unicode character offsets")
	}
	last := recallTestCall(t, backend, ctx, "tool", map[string]any{"tool_name": "supermemory-read-document", "arguments": map[string]any{"document_id": stored["document_id"], "offset": page["next_offset"]}})
	if last["content"] != "界" || last["next_offset"] != nil {
		t.Fatal("last Unicode page is incorrect")
	}
	for _, offset := range []any{"zero", -1, 0.5} {
		if _, err := backend.Call(ctx, "tool", map[string]any{"tool_name": "supermemory-read-document", "arguments": map[string]any{"document_id": doc, "offset": offset}}); err == nil {
			t.Fatalf("invalid offset accepted: %v", offset)
		}
	}
}

func TestSupermemoryFailuresDoNotReplayWrites(t *testing.T) {
	for _, status := range []int{401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Method != http.MethodPatch {
					t.Error("write was replayed as POST after non-404 failure")
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"supermemory-key"}`))
			}))
			defer server.Close()
			backend, err := NewSupermemory(config.MemoryBackendConfig{Endpoint: server.URL, APIKey: "supermemory-key", AllowPrivate: true}, toolkit.Host{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = backend.Close() }()
			_, err = backend.Call(recallTestIdentity("alice", "dm", true), "tool", map[string]any{"tool_name": "supermemory-save", "arguments": map[string]any{"content": "I prefer tea"}})
			if err == nil || strings.Contains(err.Error(), "supermemory-key") {
				t.Fatalf("unsafe write error: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("failed external write was replayed")
			}
		})
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	backend, err := NewSupermemory(config.MemoryBackendConfig{Endpoint: server.URL, APIKey: "supermemory-key", AllowPrivate: true}, toolkit.Host{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	ctx, cancel := context.WithCancel(recallTestIdentity("alice", "dm", true))
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := backend.Call(ctx, "initialize", nil); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("profile request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled profile lost cause: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("profile cancellation did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("profile cancellation did not reach server")
	}
}

func TestSupermemoryConfigurationAndInvalidResponses(t *testing.T) {
	t.Setenv("SUPERMEMORY_API_KEY", "")
	if _, err := NewSupermemory(config.MemoryBackendConfig{}, toolkit.Host{}); err == nil {
		t.Fatal("missing API credential not reported")
	}
	for _, options := range []map[string]any{{"container_tag": "shared"}, {"search_mode": "v3"}, {"max_recall_results": 21}, {"profile_frequency": 0}, {"dreaming": "fake"}, {"entity_context": strings.Repeat("界", 1501)}} {
		if _, err := NewSupermemory(config.MemoryBackendConfig{APIKey: "key", Options: options}, toolkit.Host{}); err == nil {
			t.Fatalf("invalid options accepted: %v", options)
		}
	}
	for _, body := range []string{`null`, `{"profile":{}}`, `{"profile":{"static":[],"dynamic":[]}}`, `not-json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			backend, err := NewSupermemory(config.MemoryBackendConfig{Endpoint: server.URL, APIKey: "key", AllowPrivate: true}, toolkit.Host{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = backend.Close() }()
			if _, err := backend.Call(recallTestIdentity("alice", "dm", true), "initialize", nil); err == nil {
				t.Fatal("malformed v5 profile response accepted")
			}
		})
	}
}
