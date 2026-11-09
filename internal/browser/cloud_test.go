package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func TestBrowserCloudPrivateEndpointRequiresConsent(t *testing.T) {
	const privateEndpoint = "ws://127.0.0.1:9/devtools/browser/private"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !strings.HasSuffix(r.URL.Path, "/private-session") {
			_, _ = w.Write([]byte(`{"id":"private-session","connectUrl":"` + privateEndpoint + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	newService := func() *Service {
		cfg := config.DefaultBrowserConfig()
		cfg.Provider = "browserbase"
		cfg.APIKey = "test-key"
		cfg.ProjectID = "project"
		cfg.APIURL = server.URL
		return &Service{cfg: cfg}
	}
	source, err := newService().cloud(context.Background())
	if err == nil || !strings.Contains(err.Error(), "private network") {
		t.Fatalf("private cloud endpoint was accepted without consent: %#v %v", source, err)
	}
	if source.endpoint != "" || source.release != nil {
		t.Fatalf("rejected cloud session leaked a source: %#v", source)
	}
	allowed := newService()
	allowed.cfg.AllowPrivateURLs = true
	source, err = allowed.cloud(context.Background())
	if err != nil {
		t.Fatalf("consented private cloud endpoint was refused: %v", err)
	}
	if source.endpoint != privateEndpoint {
		t.Fatalf("cloud endpoint changed: %q", source.endpoint)
	}
	if err = source.release(); err != nil {
		t.Fatalf("consented cloud session release failed: %v", err)
	}
}

func TestBrowserCloudLifecycleLive(t *testing.T) {
	providerName := os.Getenv("SABER_BROWSER_SMOKE_PROVIDER")
	if providerName == "" {
		t.Skip("set SABER_BROWSER_SMOKE_PROVIDER=browserbase|browser-use|firecrawl with real provider credentials; creates a billable browser")
	}
	cfg := config.DefaultBrowserConfig()
	cfg.Enabled = true
	cfg.Provider = providerName
	cfg.ProjectID = os.Getenv("BROWSERBASE_PROJECT_ID")
	cfg.CDPURL = os.Getenv("SABER_BROWSER_SMOKE_CDP")
	provider, err := New(cfg, browserHost(t, func(context.Context, string, bool) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := browserIdentity()
	out, err := provider.Execute(ctx, "browser_exec", map[string]any{"session": "live-cloud", "code": `new_tab('https://example.com');print(js('document.title'));capture_screenshot();`})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(toolkit.Result)
	data := result.Data.(map[string]any)
	output := data["output"].([]any)
	if len(output) == 0 || output[0] != "Example Domain" {
		t.Fatalf("live cloud browser did not read expected real page: %#v", output)
	}
	if len(result.Images) != 1 {
		t.Fatal("cloud screenshot did not enter image result")
	}
	if providerName == "browserbase" {
		if _, err = provider.Execute(ctx, "browser_exec", map[string]any{"session": "live-cloud", "code": `js("setTimeout(()=>{window.saberAnswer=prompt('Supervisor prompt','initial')},50)")`}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		var id string
		for time.Now().Before(deadline) {
			pending, e := provider.Execute(ctx, "browser_dialog", map[string]any{"session": "live-cloud", "action": "list"})
			if e != nil {
				t.Fatal(e)
			}
			dialogs, _ := pending.([]any)
			if len(dialogs) > 0 {
				id = dialogs[0].(map[string]any)["dialog_id"].(string)
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if id == "" {
			t.Fatal("real Browserbase synchronous dialog was not captured")
		}
		if _, err = provider.Execute(ctx, "browser_dialog", map[string]any{"session": "live-cloud", "action": "accept", "dialog_id": id, "prompt_text": "approved"}); err != nil {
			t.Fatal(err)
		}
		out, err = provider.Execute(ctx, "browser_exec", map[string]any{"session": "live-cloud", "code": `print(js('window.saberAnswer'))`})
		if err != nil {
			t.Fatal(err)
		}
		answer := out.(toolkit.Result).Data.(map[string]any)["output"].([]any)
		if len(answer) != 1 || answer[0] != "approved" {
			t.Fatalf("prompt value was not returned to real page JavaScript: %#v", answer)
		}
	}
	if _, err = provider.Execute(ctx, "browser_session", map[string]any{"session": "live-cloud", "action": "close"}); err != nil {
		t.Fatal(err)
	}
}
