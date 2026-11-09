package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func TestBrowserConnectedTabsRequireAdministrator(t *testing.T) {
	cfg := config.DefaultBrowserConfig()
	cfg.Enabled = true
	cfg.Provider = "cdp"
	cfg.CDPURL = "http://127.0.0.1:1"
	cfg.AttachExistingTabs = true
	provider, err := New(cfg, browserHost(t, func(context.Context, string, bool) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	if _, err = provider.Execute(browserIdentity(), "browser_exec", map[string]any{"code": "print(tabs())"}); err == nil {
		t.Fatal("nonadministrator was allowed to adopt operator browser tabs")
	}
}

func TestBrowserConnectedCleanupPreservesUserTabLive(t *testing.T) {
	endpoint := os.Getenv("SABER_BROWSER_SMOKE_CDP")
	if endpoint == "" {
		t.Skip("set SABER_BROWSER_SMOKE_CDP to a dedicated disposable Chromium CDP endpoint")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("<html><title>Connected ownership test</title><body>Keep this tab</body></html>"))
		if err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	ctx := browserIdentity()
	fixture, err := dialCDP(ctx, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fixture.close() }()
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err = fixture.call(ctx, "", "Target.createTarget", map[string]any{"url": server.URL}, &created); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := fixture.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": created.TargetID}, nil); err != nil {
			t.Error(err)
		}
	}()
	cfg := config.DefaultBrowserConfig()
	cfg.Enabled = true
	cfg.Provider = "cdp"
	cfg.CDPURL = endpoint
	cfg.AttachExistingTabs = true
	cfg.AllowPrivateURLs = true
	host := browserHost(t, func(context.Context, string, bool) error { return nil })
	host.IsAdmin = func(context.Context) bool { return true }
	provider, err := New(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	out, err := provider.Execute(ctx, "browser_exec", map[string]any{"session": "connected", "code": "use_tab(" + jsLiteral(created.TargetID) + ");wait_for_load();print(js('document.title'))"})
	if err != nil {
		t.Fatal(err)
	}
	output := out.(toolkit.Result).Data.(map[string]any)["output"].([]any)
	if len(output) != 1 || output[0] != "Connected ownership test" {
		t.Fatalf("connected tab was not actually read: %#v", output)
	}
	if err = provider.Close(); err != nil {
		t.Fatal(err)
	}
	var info struct {
		TargetInfo struct {
			TargetID string `json:"targetId"`
		} `json:"targetInfo"`
	}
	if err = fixture.call(ctx, "", "Target.getTargetInfo", map[string]any{"targetId": created.TargetID}, &info); err != nil {
		t.Fatalf("provider cleanup closed a user-owned tab: %v", err)
	}
	if info.TargetInfo.TargetID != created.TargetID {
		t.Fatal("connected user tab identity changed")
	}
}
