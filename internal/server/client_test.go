package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClient_EventCursorAndErrors(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/failure" {
			http.Error(w, "service offline", http.StatusServiceUnavailable)
			return
		}
		require.Equal(t, "17", r.Header.Get("Last-Event-ID"))
		_, err := fmt.Fprint(w, "id: 18\nevent: event\ndata: {\"id\":18,\"kind\":\"text_delta\",\"text\":\"你好\"}\n\nevent: task\ndata: {\"id\":1,\"status\":\"completed\",\"content\":\"你好\"}\n\n")
		require.NoError(t, err)
	}))
	defer endpoint.Close()
	client, err := NewClient(endpoint.URL, "token")
	require.NoError(t, err)
	stream, err := client.Subscribe(context.Background(), "session", 1, 17)
	require.NoError(t, err)
	defer func() { require.NoError(t, stream.Close()) }()
	update, err := stream.Next()
	require.NoError(t, err)
	require.EqualValues(t, 18, update.Event.ID)
	require.Equal(t, "你好", update.Event.Text)
	update, err = stream.Next()
	require.NoError(t, err)
	require.Equal(t, "completed", update.Task.Status)
	_, err = stream.Next()
	require.ErrorIs(t, err, io.EOF)
	require.ErrorContains(t, client.JSON(context.Background(), "GET", "/failure", nil, new(Info)), "service offline")
	for _, endpoint := range []string{"https://example.com", "http://127.0.0.1:8320?token=x", "http://user@127.0.0.1:8320", "http://10.0.0.1:8320"} {
		_, err := NewClient(endpoint, "token")
		require.Error(t, err)
	}
}
