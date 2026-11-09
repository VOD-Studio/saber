package memoryproviders

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func TestRetainDB_OfficialMemoryAndMultipartFileProtocol(t *testing.T) {
	ctx := nativeMemoryTestContext("alice")
	scope, err := ScopedIdentity(ctx)
	require.NoError(t, err)
	session, err := nativeLocalSession(ctx)
	require.NoError(t, err)
	var mu sync.Mutex
	var memory map[string]any
	var file map[string]any
	var fileBytes []byte
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, "Bearer retain-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		write := func(value any) { require.NoError(t, json.NewEncoder(w).Encode(value)) }
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/memory/profile/"):
			require.Equal(t, "/v1/memory/profile/"+scope, r.URL.Path)
			write(map[string]any{"user_id": scope, "memories": []any{}, "count": 0})
		case r.Method == "POST" && r.URL.Path == "/v1/memory":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, scope, body["user_id"])
			require.Equal(t, session, body["session_id"])
			require.Equal(t, "USER", body["scope_target"])
			memory = map[string]any{"id": "memory_1", "content": body["content"], "user_id": scope}
			write(map[string]any{"success": true, "mode": "async", "trace_id": "trace_1"})
		case r.Method == "POST" && r.URL.Path == "/v1/memory/search":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, scope, body["user_id"])
			require.Equal(t, []any{"USER"}, body["scope_targets"])
			require.Equal(t, false, body["include_chunks"])
			write(map[string]any{"results": []any{map[string]any{"memory": memory, "score": 0.9}}, "count": 1})
		case r.Method == "POST" && r.URL.Path == "/v1/memory/ingest/session":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, scope, body["user_id"])
			require.Equal(t, session, body["session_id"])
			require.NotEmpty(t, body["messages"])
			write(map[string]any{"success": true, "mode": body["write_mode"], "trace_id": "trace_checkpoint"})
		case r.Method == "GET" && r.URL.Path == "/v1/memory/memory_1":
			write(map[string]any{"success": true, "memory": memory})
		case r.Method == "DELETE" && r.URL.Path == "/v1/memory/memory_1":
			deleted = true
			write(map[string]any{"success": true, "memory_id": "memory_1"})
		case r.Method == "POST" && r.URL.Path == "/v1/files":
			require.NoError(t, r.ParseMultipartForm(1024*1024))
			if r.MultipartForm != nil {
				defer func() { require.NoError(t, r.MultipartForm.RemoveAll()) }()
			}
			uploaded, header, err := r.FormFile("file")
			require.NoError(t, err)
			defer func() { require.NoError(t, uploaded.Close()) }()
			require.Equal(t, "notes.txt", header.Filename)
			fileBytes, err = io.ReadAll(uploaded)
			require.NoError(t, err)
			var metadata map[string]any
			require.NoError(t, json.Unmarshal([]byte(r.FormValue("metadata")), &metadata))
			require.Equal(t, scope, metadata["saber_scope"])
			require.Equal(t, "USER", r.FormValue("scope"))
			require.Equal(t, "false", r.FormValue("is_public"))
			file = map[string]any{"id": "file_1", "path": r.FormValue("path"), "metadata": metadata, "name": "notes.txt"}
			write(map[string]any{"success": true, "file": file})
		case r.Method == "GET" && r.URL.Path == "/v1/files":
			require.Equal(t, "/saber/"+scope+"/", r.URL.Query().Get("prefix"))
			write(map[string]any{"success": true, "files": []any{file}, "total": 1})
		case r.Method == "GET" && r.URL.Path == "/v1/files/file_1":
			write(map[string]any{"success": true, "file": file})
		case r.Method == "GET" && r.URL.Path == "/v1/files/file_1/content":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write(fileBytes)
		case r.Method == "POST" && r.URL.Path == "/v1/files/file_1/ingest":
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, scope, body["user_id"])
			write(map[string]any{"success": true, "memories_created": 1})
		case r.Method == "DELETE" && r.URL.Path == "/v1/files/file_1":
			require.Equal(t, "true", r.URL.Query().Get("hard"))
			write(map[string]any{"success": true})
		default:
			t.Errorf("unexpected RetainDB route %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	backend, err := NewRetainDB(config.MemoryBackendConfig{Endpoint: server.URL, APIKey: "retain-key", AllowPrivate: true}, toolkit.Host{})
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	call := func(operation string, payload map[string]any) any {
		value, err := backend.Call(ctx, operation, nativeMemoryEnvelope(payload))
		require.NoError(t, err)
		return value
	}
	call("initialize", nil)
	call("retaindb_remember", map[string]any{"content": "User prefers jasmine tea", "memory_type": "preference", "importance": 0.8})
	contextValue := call("context", map[string]any{"query": "tea"}).(map[string]any)
	require.Contains(t, contextValue["context"], "jasmine tea")
	turn := map[string]any{"user_content": "I prefer jasmine tea", "assistant_content": "Noted", "status": "completed"}
	call("sync_turn", turn)
	checkpoint := call("pre_compress", turn).(map[string]any)
	require.Equal(t, true, checkpoint["checkpointed"])
	require.Equal(t, "trace_checkpoint", checkpoint["checkpoint_id"])
	call("retaindb_upload_file", map[string]any{"name": "notes.txt", "path": "notes.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("real uploaded notes"))})
	listed := call("retaindb_list_files", map[string]any{}).(map[string]any)
	require.Len(t, listed["files"], 1)
	read := call("retaindb_read_file", map[string]any{"file_id": "file_1"}).(map[string]any)
	decoded, err := base64.StdEncoding.DecodeString(read["content_base64"].(string))
	require.NoError(t, err)
	require.Equal(t, "real uploaded notes", string(decoded))
	call("retaindb_ingest_file", map[string]any{"file_id": "file_1"})
	call("retaindb_delete_file", map[string]any{"file_id": "file_1"})
	call("retaindb_forget", map[string]any{"memory_id": "memory_1"})
	mu.Lock()
	wasDeleted := deleted
	mu.Unlock()
	require.True(t, wasDeleted)
	tools := call("tools", nil)
	require.Len(t, tools, 10)
}

func TestRetainDB_RejectsCrossScopeMutationAndUnsafePaths(t *testing.T) {
	var mu sync.Mutex
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mu.Lock()
			mutations++
			mu.Unlock()
		}
		switch r.URL.Path {
		case "/v1/memory/foreign":
			_, _ = w.Write([]byte(`{"success":true,"memory":{"id":"foreign","user_id":"someone-else"}}`))
		case "/v1/files/foreign":
			_, _ = w.Write([]byte(`{"success":true,"file":{"id":"foreign","path":"/saber/other/notes.txt","metadata":{"saber_scope":"other"}}}`))
		default:
			http.Error(w, "secret must not appear in errors", http.StatusForbidden)
		}
	}))
	defer server.Close()
	backend, err := NewRetainDB(config.MemoryBackendConfig{Endpoint: server.URL, APIKey: "retain-secret", AllowPrivate: true}, toolkit.Host{})
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	ctx := nativeMemoryTestContext("alice")
	_, err = backend.Call(ctx, "retaindb_forget", nativeMemoryEnvelope(map[string]any{"memory_id": "foreign"}))
	require.ErrorContains(t, err, "outside current trusted scope")
	for _, operation := range []string{"retaindb_read_file", "retaindb_ingest_file", "retaindb_delete_file"} {
		_, err = backend.Call(ctx, operation, nativeMemoryEnvelope(map[string]any{"file_id": "foreign"}))
		require.ErrorContains(t, err, "outside current trusted scope")
	}
	mu.Lock()
	count := mutations
	mu.Unlock()
	require.Zero(t, count)
	_, err = backend.Call(ctx, "retaindb_upload_file", nativeMemoryEnvelope(map[string]any{"name": "notes.txt", "path": "../escape", "content_base64": ""}))
	require.ErrorContains(t, err, "escape")
	_, err = backend.Call(ctx, "retaindb_search", nativeMemoryEnvelope(map[string]any{"query": "tea"}))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "retain-secret")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = backend.Call(canceled, "initialize", nil)
	require.ErrorIs(t, err, context.Canceled)
}
