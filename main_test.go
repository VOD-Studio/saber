package main

import (
	"testing"
)

func TestVersionVariables(t *testing.T) {
	if version == "" {
		t.Error("version should not be empty")
	}
	if gitCommit == "" {
		t.Error("gitCommit should not be empty")
	}
	if gitBranch == "" {
		t.Error("gitBranch should not be empty")
	}
	if buildTime == "" {
		t.Error("buildTime should not be empty")
	}
	if goVersion == "" {
		t.Error("goVersion should not be empty")
	}
	if platform == "" {
		t.Error("platform should not be empty")
	}
}

func TestDefaultVersionValues(t *testing.T) {
	if version != "dev" {
		t.Errorf("default version = %q, want %q", version, "dev")
	}
	if gitCommit != "unknown" {
		t.Errorf("default gitCommit = %q, want %q", gitCommit, "unknown")
	}
	if gitBranch != "unknown" {
		t.Errorf("default gitBranch = %q, want %q", gitBranch, "unknown")
	}
	if buildTime != "unknown" {
		t.Errorf("default buildTime = %q, want %q", buildTime, "unknown")
	}
	if goVersion != "unknown" {
		t.Errorf("default goVersion = %q, want %q", goVersion, "unknown")
	}
	if platform != "unknown" {
		t.Errorf("default platform = %q, want %q", platform, "unknown")
	}
}
