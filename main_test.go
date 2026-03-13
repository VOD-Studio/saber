package main

import (
	"testing"
)

func TestVersionVariables(t *testing.T) {
	if version == "" {
		t.Error("version should not be empty")
	}
	if gitMsg == "" {
		t.Error("gitMsg should not be empty")
	}
}

func TestDefaultVersionValues(t *testing.T) {
	if version != "dev" {
		t.Errorf("default version = %q, want %q", version, "dev")
	}
	if gitMsg != "unknown" {
		t.Errorf("default gitMsg = %q, want %q", gitMsg, "unknown")
	}
}
