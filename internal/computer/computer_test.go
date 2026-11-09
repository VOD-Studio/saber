package computer

import (
	"context"
	"errors"
	"os"
	"testing"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func computerIdentity() context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: chat.Session{Platform: "test", Account: "local", Conversation: "computer"}, SenderID: "owner", Direct: true})
}
func computerHost(t *testing.T, check func(context.Context, string, bool) error) toolkit.Host {
	t.Helper()
	workspace := t.TempDir()
	return toolkit.Host{StateDir: t.TempDir(), Workspace: func(context.Context) (string, error) { return workspace, nil }, Check: check}
}

func TestComputerPermissionDeniedBeforeDriver(t *testing.T) {
	cfg := config.DefaultComputerConfig()
	cfg.Enabled = true
	cfg.AllowHostProcess = true
	cfg.Command = "/not-a-driver"
	denied := errors.New("permission denied")
	provider, err := New(cfg, computerHost(t, func(context.Context, string, bool) error { return denied }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	_, err = provider.Execute(computerIdentity(), "computer_use", map[string]any{"action": "capture"})
	if !errors.Is(err, denied) {
		t.Fatalf("authorization was not checked before launch: %v", err)
	}
}

func TestComputerRefusesInputWithoutCapture(t *testing.T) {
	s := &Service{cfg: config.DefaultComputerConfig()}
	for _, action := range []string{"click", "drag", "scroll", "type", "key", "set_value"} {
		if _, err := s.action(context.Background(), &computerSession{}, action, map[string]any{"text": "not dispatched"}); err == nil {
			t.Errorf("input %s accepted without real target state", action)
		}
	}
}

func TestComputerModeCannotBeOverriddenByArgs(t *testing.T) {
	cfg := config.DefaultComputerConfig()
	cfg.Enabled = true
	cfg.AllowHostProcess = true
	for _, args := range [][]string{{"mcp", "--dangerously-bypass-approvals"}, {"mcp", "--permission-mode=unrestricted"}, {"mcp", "--socket", "foreign"}} {
		cfg.Args = args
		if err := cfg.Validate(); err == nil {
			t.Errorf("mode/lifecycle override accepted: %v", args)
		}
	}
	cfg.Args = nil
	cfg.PermissionMode = "bounded"
	cfg.CapabilityManifest = "reviewed.yaml"
	if err := cfg.Validate(); err == nil {
		t.Fatal("bounded manifest was approved without explicit consent")
	}
	cfg.PermissionMode = "unrestricted"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unrestricted mode accepted without risk opt-in")
	}
}

func TestComputerRealDriverCaptureAndCancellation(t *testing.T) {
	if os.Getenv("SABER_CUA_SMOKE") != "1" {
		t.Skip("set SABER_CUA_SMOKE=1 with cua-driver >=0.20 and a permissioned reachable desktop")
	}
	cfg := config.DefaultComputerConfig()
	cfg.Enabled = true
	cfg.AllowHostProcess = true
	if command := os.Getenv("SABER_CUA_SMOKE_COMMAND"); command != "" {
		cfg.Command = command
	}
	cfg.Endpoint = os.Getenv("SABER_CUA_SMOKE_ENDPOINT")
	provider, err := New(cfg, computerHost(t, func(context.Context, string, bool) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := computerIdentity()
	if _, err = provider.Execute(ctx, "computer_use", map[string]any{"action": "doctor"}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"action": "capture", "mode": "som"}
	if app := os.Getenv("SABER_CUA_SMOKE_APP"); app != "" {
		args["app"] = app
	}
	out, err := provider.Execute(ctx, "computer_use", args)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(toolkit.Result)
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("driver did not return structured capture: %#v", result.Data)
	}
	if pid, ok := integer(data["pid"]); !ok || pid <= 0 {
		t.Fatal("capture did not identify a real app pid")
	}
	if window, ok := integer(data["window_id"]); !ok || window <= 0 {
		t.Fatal("capture did not identify a real window")
	}
	if len(result.Images) == 0 {
		t.Fatal("real desktop capture image missing")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = provider.Execute(cancelled, "computer_use", map[string]any{"action": "wait", "seconds": 10.0})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
	if _, err = provider.Execute(ctx, "computer_use", map[string]any{"action": "close"}); err != nil {
		t.Fatal(err)
	}
}
