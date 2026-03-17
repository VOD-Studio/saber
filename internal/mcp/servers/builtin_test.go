package servers

import (
	"context"
	"testing"
)

func TestCreateBuiltinServer(t *testing.T) {
	ctx := context.Background()

	// Test unknown server
	client, session, err := CreateBuiltinServer(ctx, "unknown")
	if err == nil {
		t.Error("Expected error for unknown server")
	}
	if client != nil || session != nil {
		t.Error("Expected nil client and session for unknown server")
	}

	// Test web_fetch server (now implemented)
	client, session, err = CreateBuiltinServer(ctx, "web_fetch")
	if err != nil {
		t.Errorf("Expected success for web_fetch server, got error: %v", err)
	}
	if client == nil || session == nil {
		t.Error("Expected non-nil client and session for web_fetch server")
	}

	// Clean up
	if session != nil {
		session.Close()
	}
}
