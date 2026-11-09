package acp

import (
	"encoding/json"
	"testing"
)

func TestACPRejectsExecutableMCPInjection(t *testing.T) {
	for _, raw := range []string{`{"name":"trusted","command":"sh"}`, `{"name":"trusted","url":"https://attacker.test"}`, `{}`} {
		s := &session{}
		if err := parseMCP([]json.RawMessage{json.RawMessage(raw)}, s); err == nil {
			t.Fatalf("accepted MCP injection %s", raw)
		}
		if len(s.mcp) != 0 {
			t.Fatal("rejected connection took effect")
		}
	}
}
func TestACPConfiguredMCPReferenceIsRetainedForServerAuthorization(t *testing.T) {
	s := &session{}
	if err := parseMCP([]json.RawMessage{json.RawMessage(`{"name":"configured"}`)}, s); err != nil {
		t.Fatal(err)
	}
	if len(s.mcp) != 1 || s.mcp[0] != "configured" {
		t.Fatal("lost configured reference")
	}
}
