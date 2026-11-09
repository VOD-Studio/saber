package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/websocket"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func browserIdentity() context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: chat.Session{Platform: "test", Account: "local", Conversation: "browser"}, SenderID: "owner", Direct: true})
}
func browserHost(t *testing.T, check func(context.Context, string, bool) error) toolkit.Host {
	t.Helper()
	workspace := t.TempDir()
	return toolkit.Host{StateDir: t.TempDir(), Workspace: func(context.Context) (string, error) { return workspace, nil }, Check: check}
}

func TestBrowserPermissionDeniedBeforeLaunch(t *testing.T) {
	cfg := config.DefaultBrowserConfig()
	cfg.Enabled = true
	cfg.AllowHostProcess = true
	cfg.ExecutablePath = "/not-a-browser"
	denied := errors.New("permission denied")
	provider, err := New(cfg, browserHost(t, func(context.Context, string, bool) error { return denied }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	_, err = provider.Execute(browserIdentity(), "browser_exec", map[string]any{"code": "new_tab('https://example.com')"})
	if !errors.Is(err, denied) {
		t.Fatalf("expected authorization refusal, got %v", err)
	}
}

func TestBrowserMetadataAndPrivateURLPolicy(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"http://169.254.169.254/latest/meta-data", "http://100.100.100.200/", "https://metadata.google.internal/", "file:///etc/passwd", "http://user:secret@example.com/"} {
		if _, err := urlPrivate(ctx, raw); err == nil {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
	private, err := urlPrivate(ctx, "http://127.0.0.1:80/")
	if err != nil || !private {
		t.Fatalf("loopback policy did not recognize private target: private=%v err=%v", private, err)
	}
	b := &browserSession{owner: &Service{cfg: config.DefaultBrowserConfig()}}
	if err = b.checkURL(ctx, "http://127.0.0.1/"); err == nil {
		t.Fatal("private target bypassed opt-in")
	}
	b.owner.cfg.AllowPrivateURLs = true
	if err = b.checkURL(ctx, "http://169.254.169.254/"); err == nil {
		t.Fatal("metadata was permitted by private opt-in")
	}
}

func TestBrowserWorkflowRealChromium(t *testing.T) {
	binary := os.Getenv("SABER_BROWSER_SMOKE_EXECUTABLE")
	if binary == "" {
		t.Skip("set SABER_BROWSER_SMOKE_EXECUTABLE to an installed Chromium/Chrome executable")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, err := w.Write([]byte(`<html><title>Saber browser test</title><input id="name"><button id="submit" onclick="document.getElementById('out').textContent='Hello '+document.getElementById('name').value">Submit</button><p id="out"></p></html>`))
		if err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg := config.DefaultBrowserConfig()
	cfg.Enabled = true
	cfg.AllowHostProcess = true
	cfg.AllowPrivateURLs = true
	cfg.ExecutablePath = binary
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
	code := `var tab=new_tab(` + jsLiteral(server.URL) + `);fill_input('#name','Ada');click('#submit');print(js("document.getElementById('out').textContent"));capture_screenshot();print(tab);`
	out, err := provider.Execute(ctx, "browser_exec", map[string]any{"session": "workflow", "code": code})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(toolkit.Result)
	data := result.Data.(map[string]any)
	output := data["output"].([]any)
	if len(output) < 2 || output[0] != "Hello Ada" {
		t.Fatalf("real form interaction failed: %#v", output)
	}
	if len(result.Images) != 1 || !strings.HasPrefix(result.Images[0], "data:image/png;base64,") {
		t.Fatal("real screenshot was not delivered to model")
	}
	target := output[len(output)-1].(string)
	_, err = provider.Execute(ctx, "browser_cdp", map[string]any{"session": "other", "method": "Runtime.evaluate", "target_id": target, "params": map[string]any{"expression": "document.title"}})
	if err == nil {
		t.Fatal("one browser session accessed another session's target")
	}
	_, err = provider.Execute(ctx, "browser_exec", map[string]any{"session": "workflow", "timeout_s": 1, "code": "while(true){}"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("script cancellation lost context error: %v", err)
	}
	status, err := provider.Execute(ctx, "browser_session", map[string]any{"session": "workflow", "action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(status)
	if string(raw) != `{"active":false}` {
		t.Fatalf("cancelled browser still active: %s", raw)
	}
}

func TestBrowserCookieAccessRequiresIsolatedContext(t *testing.T) {
	recorder := &cookieCDPRecorder{}
	server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		for {
			var msg cdpMessage
			if err := websocket.JSON.Receive(ws, &msg); err != nil {
				return
			}
			var params map[string]any
			_ = json.Unmarshal(msg.Params, &params)
			recorder.record(msg.Method, params)
			if err := websocket.JSON.Send(ws, cdpMessage{ID: msg.ID, Result: json.RawMessage(`{"cookies":[{"name":"sid","value":"super-secret-cookie"}]}`)}); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/fake"
	conn, err := dialCDP(context.Background(), endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.close() }()
	service := &Service{cfg: config.DefaultBrowserConfig()}
	shared := &browserSession{owner: service, tabs: map[string]string{}}
	for _, method := range []string{"Network.getAllCookies", "Network.getCookies", "Network.setCookie", "Network.setCookies", "Network.deleteCookies", "Network.clearBrowserCookies", "Storage.getCookies", "Storage.setCookies", "Storage.clearCookies"} {
		out, err := shared.raw(context.Background(), method, map[string]any{"browserContextId": "shared-profile"}, "", "")
		if err == nil || out != nil {
			t.Fatalf("%s without an isolated context returned data: %#v %v", method, out, err)
		}
		if !strings.Contains(err.Error(), "cookie access requires an isolated browser context") {
			t.Fatalf("%s refusal does not explain the isolation requirement: %v", method, err)
		}
		if strings.Contains(err.Error(), "super-secret-cookie") {
			t.Fatalf("%s refusal echoed a cookie value: %v", method, err)
		}
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("shared profile cookie methods reached the browser: %#v", calls)
	}
	script := &browserSession{owner: service, label: "cookie", tabs: map[string]string{}, op: make(chan struct{}, 1), stop: make(chan struct{}), events: make(chan cdpMessage, 4)}
	if _, err = script.executeCode(context.Background(), map[string]any{"code": `print(cdp("Network.getAllCookies"))`}); err == nil || !strings.Contains(err.Error(), "cookie access requires an isolated browser context") {
		t.Fatalf("cdp() helper bypassed the cookie isolation requirement: %v", err)
	}
	isolated := &browserSession{owner: service, cdp: conn, contextID: "ctx-isolated", tabs: map[string]string{}}
	out, err := isolated.raw(context.Background(), "Storage.getCookies", map[string]any{"browserContextId": "shared-profile"}, "", "")
	if err != nil {
		t.Fatalf("isolated context lost Storage cookie access: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("isolated Storage.getCookies returned %#v", out)
	}
	cookies, ok := payload["cookies"].([]any)
	if !ok || len(cookies) != 1 {
		t.Fatalf("isolated Storage.getCookies result shape: %#v", payload)
	}
	call := recorder.last()
	if call["method"] != "Storage.getCookies" {
		t.Fatalf("unexpected CDP method: %#v", call)
	}
	params, _ := call["params"].(map[string]any)
	if params["browserContextId"] != "ctx-isolated" {
		t.Fatalf("caller selected the browser context: %#v", params)
	}
}

type cookieCDPRecorder struct {
	mu    sync.Mutex
	calls []map[string]any
}

func (r *cookieCDPRecorder) record(method string, params map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, map[string]any{"method": method, "params": params})
}

func (r *cookieCDPRecorder) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.calls...)
}

func (r *cookieCDPRecorder) last() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

func TestBrowserProfileSnapshotCopiesOnlyPinnedProfile(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	if err := os.WriteFile(source+"/Local State", []byte(`{"profile":{"last_used":"Default"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Default", "Profile 2"} {
		if err := os.Mkdir(source+"/"+name, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source+"/"+name+"/Preferences", []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := snapshotProfile(context.Background(), source, "Profile 2", dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest + "/Default"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unselected profile was copied")
	}
	data, err := os.ReadFile(dest + "/Profile 2/Preferences")
	if err != nil || string(data) != "Profile 2" {
		t.Fatalf("pinned profile not copied: %s %v", data, err)
	}
	if err = snapshotProfile(context.Background(), source, "missing", t.TempDir()); err == nil {
		t.Fatal("missing pin silently fell back")
	}
}

func TestBrowserUnavailableRealProfileIsExplicitlySignedOutLive(t *testing.T) {
	binary := os.Getenv("SABER_BROWSER_SMOKE_EXECUTABLE")
	if binary == "" {
		t.Skip("set SABER_BROWSER_SMOKE_EXECUTABLE to a real Chromium/Chrome binary")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("<html><body>Fresh profile fixture</body></html>"))
		if err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cfg := config.DefaultBrowserConfig()
	cfg.Enabled = true
	cfg.AllowHostProcess = true
	cfg.AllowPrivateURLs = true
	cfg.UseRealProfile = true
	cfg.RealProfileSource = t.TempDir()
	cfg.RealProfilePin = "missing"
	cfg.ExecutablePath = binary
	provider, err := New(cfg, browserHost(t, func(context.Context, string, bool) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Error(err)
		}
	}()
	out, err := provider.Execute(browserIdentity(), "browser_exec", map[string]any{"code": "new_tab(" + jsLiteral(server.URL) + ");print(js('document.cookie'))"})
	if err != nil {
		t.Fatal(err)
	}
	data := out.(toolkit.Result).Data.(map[string]any)
	if note, _ := data["browser_note"].(string); !strings.Contains(note, "real_profile_unavailable") {
		t.Fatal("real profile failure was hidden")
	}
	output := data["output"].([]any)
	if len(output) != 1 || output[0] != "" {
		t.Fatalf("fresh profile unexpectedly had saved cookies: %#v", output)
	}
}
