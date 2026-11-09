package browser

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type sourceSession struct {
	endpoint string
	release  func() error
	local    bool
	textOnly bool
	note     string
}

func privateEnv() []string {
	keys := []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TEMP", "TMP", "DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "XDG_SESSION_TYPE", "DBUS_SESSION_BUS_ADDRESS", "LANG", "LC_ALL", "SYSTEMROOT", "WINDIR"}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}

func digest(value string) string { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }

func (s *Service) source(ctx context.Context, key string, local bool) (sourceSession, error) {
	cfg := s.cfg
	if !local && cfg.CDPURL != "" {
		return sourceSession{endpoint: cfg.CDPURL, release: func() error { return nil }}, nil
	}
	if local || cfg.Provider == "local" {
		return s.launch(ctx, key, local && !strings.HasSuffix(key, "::profile"))
	}
	if cfg.Provider == "nous" {
		return sourceSession{}, errors.New("nous subscription browser requires browser.cdp_url issued by the authenticated Tool Gateway; no local fallback or fabricated paid session")
	}
	if strings.HasPrefix(cfg.Provider, "plugin:") {
		s.redactMu.RLock()
		fn := s.sourceFn
		s.redactMu.RUnlock()
		if fn == nil {
			return sourceSession{}, errors.New("browser plugin source adapter is not configured")
		}
		endpoint, release, err := fn(ctx, strings.TrimPrefix(cfg.Provider, "plugin:"), digest(key))
		if err != nil {
			return sourceSession{}, err
		}
		if release == nil {
			return sourceSession{}, errors.New("browser plugin must supply a real session release operation")
		}
		private, err := urlPrivate(ctx, endpoint)
		if err != nil || (private && !cfg.AllowPrivateURLs) {
			if err == nil {
				err = errors.New("plugin CDP endpoint targets a private network; allow_private_urls consent required")
			}
			return sourceSession{}, errors.Join(err, release())
		}
		return sourceSession{endpoint: endpoint, release: release}, nil
	}
	return s.cloud(ctx)
}

func chromiumBinary(configured string) (string, error) {
	if configured != "" {
		p, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("browser.executable_path unavailable: %w", err)
		}
		return p, nil
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "brave-browser", "microsoft-edge"} {
		if p, e := exec.LookPath(name); e == nil {
			return p, nil
		}
	}
	if runtime.GOOS == "darwin" {
		for _, p := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium", "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"} {
			if st, e := os.Stat(p); e == nil && !st.IsDir() {
				return p, nil
			}
		}
	}
	return "", errors.New("chromium not installed; install Chromium/Chrome and set browser.executable_path (Linux also needs browser system libraries)")
}

func (s *Service) launch(ctx context.Context, key string, sidecar bool) (sourceSession, error) {
	if !s.cfg.AllowHostProcess {
		return sourceSession{}, errors.New("host browser launch denied; explicitly configure browser.allow_host_process or a sandbox CDP endpoint")
	}
	root := filepath.Join(s.host.StateDir, "browser")
	if err := os.MkdirAll(root, 0700); err != nil {
		return sourceSession{}, err
	}
	if _, err := processIdentity(os.Getpid()); err != nil {
		return sourceSession{}, err
	}
	if err := reapOrphans(root); err != nil {
		return sourceSession{}, err
	}
	profile, err := os.MkdirTemp(root, "session-")
	if err != nil {
		return sourceSession{}, err
	}
	removeProfile := true
	rollback := true
	defer func() {
		if rollback && removeProfile {
			_ = os.RemoveAll(profile)
		}
	}()
	engine := s.cfg.Engine
	note := ""
	if s.cfg.UseRealProfile && !sidecar {
		engine = "chromium"
		if err = snapshotProfile(ctx, s.cfg.RealProfileSource, s.cfg.RealProfilePin, profile); err != nil {
			if ctx.Err() != nil {
				return sourceSession{}, ctx.Err()
			}
			note = fmt.Sprintf("real_profile_unavailable: %v. This is a real fresh browser WITHOUT the user's saved logins; do not claim to be signed in.", err)
			if e := os.RemoveAll(profile); e != nil {
				return sourceSession{}, errors.Join(err, e)
			}
			if e := os.MkdirAll(profile, 0700); e != nil {
				return sourceSession{}, errors.Join(err, e)
			}
		}
	} else if s.cfg.UserDataDir != "" && !sidecar {
		if err = os.RemoveAll(profile); err != nil {
			return sourceSession{}, err
		}
		profile = filepath.Join(s.cfg.UserDataDir, digest(key))
		removeProfile = false
		if err = os.MkdirAll(profile, 0700); err != nil {
			return sourceSession{}, err
		}
	}
	var binary string
	var args []string
	var endpoint string
	if engine == "lightpanda" {
		binary = s.cfg.ExecutablePath
		if binary == "" {
			binary = "lightpanda"
		}
		binary, err = exec.LookPath(binary)
		if err != nil {
			return sourceSession{}, fmt.Errorf("lightpanda unavailable; install lightpanda and set browser.executable_path: %w", err)
		}
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			return sourceSession{}, e
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if e = listener.Close(); e != nil {
			return sourceSession{}, e
		}
		args = []string{"serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
		endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
		if !s.cfg.AllowPrivateURLs {
			args = append(args, "--block-private-networks")
		}
	} else {
		binary, err = chromiumBinary(s.cfg.ExecutablePath)
		if err != nil {
			return sourceSession{}, err
		}
		args = []string{"--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-component-update", "--disable-sync"}
		if !s.cfg.Headed || (runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "") {
			args = append(args, "--headless=new")
		}
		args = append(args, s.cfg.ChromiumArgs...)
		args = append(args, "about:blank")
		if err = os.Remove(filepath.Join(profile, "DevToolsActivePort")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return sourceSession{}, err
		}
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = privateEnv()
	cmd.Dir = profile
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = configureProcess(cmd); err != nil {
		return sourceSession{}, err
	}
	if err = cmd.Start(); err != nil {
		return sourceSession{}, fmt.Errorf("browser launch failed: %w", err)
	}
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(done) }()
	recordPath, recordErr := recordProcess(root, cmd.Process.Pid, profile, removeProfile)
	if recordErr != nil {
		_ = killOwnedProcess(cmd.Process.Pid)
		<-done
		return sourceSession{}, recordErr
	}
	release := func() error {
		var stopErr error
		select {
		case <-done:
		default:
			if e := killOwnedProcess(cmd.Process.Pid); e != nil && !errors.Is(e, os.ErrProcessDone) {
				stopErr = e
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				stopErr = errors.Join(stopErr, errors.New("browser process did not terminate"))
			}
		}
		if stopErr == nil {
			if removeProfile {
				stopErr = errors.Join(stopErr, os.RemoveAll(profile))
			}
			if e := os.Remove(recordPath); e != nil && !errors.Is(e, os.ErrNotExist) {
				stopErr = errors.Join(stopErr, e)
			}
		}
		return stopErr
	}
	deadline := time.NewTimer(time.Duration(s.cfg.CommandTimeout) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if engine == "lightpanda" {
			u, _ := url.Parse(endpoint)
			conn, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", u.Host)
			if e == nil {
				_ = conn.Close()
				rollback = false
				return sourceSession{endpoint: endpoint, release: release, local: true, textOnly: true}, nil
			}
		} else {
			data, e := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
			if e == nil {
				parts := strings.Fields(string(data))
				if len(parts) == 2 {
					port, e := strconv.Atoi(parts[0])
					if e == nil && port > 0 && port < 65536 && strings.HasPrefix(parts[1], "/devtools/browser/") {
						rollback = false
						return sourceSession{endpoint: "ws://127.0.0.1:" + parts[0] + parts[1], release: release, local: true, note: note}, nil
					}
				}
			}
		}
		select {
		case <-done:
			_ = release()
			return sourceSession{}, fmt.Errorf("browser exited before CDP became ready (sandbox/display/system libraries): %w", exitErr)
		case <-ctx.Done():
			_ = release()
			return sourceSession{}, ctx.Err()
		case <-deadline.C:
			_ = release()
			return sourceSession{}, errors.New("browser CDP startup timed out; check executable, profile locks and Linux libraries")
		case <-ticker.C:
		}
	}
}

func snapshotProfile(ctx context.Context, source, pin, dest string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	localState, err := os.ReadFile(filepath.Join(source, "Local State"))
	if err != nil {
		return err
	}
	if pin == "" {
		var state struct {
			Profile struct {
				LastUsed string `json:"last_used"`
			} `json:"profile"`
		}
		if err = json.Unmarshal(localState, &state); err != nil {
			return err
		}
		pin = state.Profile.LastUsed
		if pin == "" {
			pin = "Default"
		}
	}
	if strings.ContainsAny(pin, "/\\") || pin == "." || pin == ".." {
		return errors.New("invalid active real profile directory")
	}
	profile := filepath.Join(source, pin)
	if st, e := os.Lstat(profile); e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("real profile pin does not name a real directory")
	}
	var state map[string]any
	if err = json.Unmarshal(localState, &state); err != nil {
		return err
	}
	profileState, _ := state["profile"].(map[string]any)
	if profileState == nil {
		profileState = make(map[string]any)
		state["profile"] = profileState
	}
	profileState["last_used"] = pin
	localState, err = json.Marshal(state)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dest, "Local State"), localState, 0600); err != nil {
		return err
	}
	return filepath.WalkDir(profile, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		rel, e := filepath.Rel(profile, path)
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("profile symlink refused: %s", rel)
		}
		if d.IsDir() && (d.Name() == "Cache" || d.Name() == "Code Cache" || d.Name() == "GPUCache" || d.Name() == "Service Worker") {
			return filepath.SkipDir
		}
		target := filepath.Join(dest, pin, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if strings.HasPrefix(d.Name(), "Singleton") || strings.HasSuffix(d.Name(), "-wal") || strings.HasSuffix(d.Name(), "-shm") || strings.HasSuffix(d.Name(), "-journal") {
			return nil
		}
		file, e := os.Open(path)
		if e != nil {
			return e
		}
		defer func() { _ = file.Close() }()
		var header [16]byte
		n, _ := file.Read(header[:])
		if _, e = file.Seek(0, io.SeekStart); e != nil {
			return e
		}
		if n == 16 && string(header[:]) == "SQLite format 3\x00" {
			u := url.URL{Scheme: "file", Path: path}
			q := u.Query()
			q.Set("mode", "ro")
			q.Set("_pragma", "busy_timeout(2000)")
			u.RawQuery = q.Encode()
			db, e := sql.Open("sqlite", u.String())
			if e != nil {
				return e
			}
			defer func() { _ = db.Close() }()
			_, e = db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(target, "'", "''")+"'")
			if e != nil {
				return fmt.Errorf("online profile database snapshot %s: %w", rel, e)
			}
			return os.Chmod(target, 0600)
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, copyErr := io.Copy(out, file)
		return errors.Join(copyErr, out.Close())
	})
}

func (s *Service) cloud(ctx context.Context) (sourceSession, error) {
	cfg := s.cfg
	key := cfg.APIKey
	if key == "" && cfg.APIKeyEnv != "" {
		key = os.Getenv(cfg.APIKeyEnv)
	}
	defaults := map[string]string{"browserbase": "BROWSERBASE_API_KEY", "browser-use": "BROWSER_USE_API_KEY", "firecrawl": "FIRECRAWL_API_KEY"}
	if key == "" {
		key = os.Getenv(defaults[cfg.Provider])
	}
	if key == "" {
		return sourceSession{}, fmt.Errorf("selected %s browser requires browser.api_key or %s; no fallback provider was invoked", cfg.Provider, defaults[cfg.Provider])
	}
	s.remember(key)
	base := cfg.APIURL
	var path string
	body := make(map[string]any)
	for k, v := range cfg.CloudOptions {
		body[k] = v
	}
	header := make(http.Header)
	switch cfg.Provider {
	case "browserbase":
		if base == "" {
			base = "https://api.browserbase.com"
		}
		path = "/v1/sessions"
		header.Set("X-BB-API-Key", key)
		if cfg.ProjectID == "" {
			return sourceSession{}, errors.New("browserbase requires browser.project_id")
		}
		body["projectId"] = cfg.ProjectID
		if cfg.CloudProfileID != "" {
			settings := make(map[string]any)
			if configured, ok := body["browserSettings"].(map[string]any); ok {
				for key, value := range configured {
					settings[key] = value
				}
			}
			settings["context"] = map[string]any{"id": cfg.CloudProfileID, "persist": true}
			body["browserSettings"] = settings
		}
	case "browser-use":
		if base == "" {
			base = "https://api.browser-use.com"
		}
		path = "/api/v2/browsers"
		header.Set("X-Browser-Use-API-Key", key)
		if cfg.CloudProfileID != "" {
			body["profileId"] = cfg.CloudProfileID
		}
	case "firecrawl":
		if base == "" {
			base = "https://api.firecrawl.dev"
		}
		path = "/v2/interact"
		header.Set("Authorization", "Bearer "+key)
		if cfg.CloudProfileID != "" {
			body["profile"] = map[string]any{"name": cfg.CloudProfileID, "saveChanges": true}
		}
	default:
		return sourceSession{}, fmt.Errorf("unsupported cloud provider %q", cfg.Provider)
	}
	var result map[string]any
	if err := cloudRequest(ctx, http.MethodPost, base+path, header, body, &result); err != nil {
		return sourceSession{}, err
	}
	payload := result
	if nested, ok := result["data"].(map[string]any); ok {
		payload = nested
	}
	id, _ := payload["id"].(string)
	endpoint, _ := payload["connectUrl"].(string)
	if endpoint == "" {
		endpoint, _ = payload["cdpUrl"].(string)
	}
	if endpoint == "" {
		endpoint, _ = payload["cdp_url"].(string)
	}
	release := func() error {
		closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		switch cfg.Provider {
		case "browserbase":
			return cloudRequest(closeCtx, http.MethodPost, base+path+"/"+url.PathEscape(id), header, map[string]any{"projectId": cfg.ProjectID, "status": "REQUEST_RELEASE"}, nil)
		case "browser-use":
			return cloudRequest(closeCtx, http.MethodPatch, base+path+"/"+url.PathEscape(id), header, map[string]any{"action": "stop"}, nil)
		default:
			return cloudRequest(closeCtx, http.MethodDelete, base+path+"/"+url.PathEscape(id), header, nil, nil)
		}
	}
	if id == "" {
		return sourceSession{}, errors.New("cloud browser response did not contain session id; creation outcome unknown, inspect provider sessions")
	}
	if endpoint == "" {
		return sourceSession{}, errors.Join(errors.New("cloud browser response did not contain a CDP endpoint"), release())
	}
	private, err := urlPrivate(ctx, endpoint)
	if err != nil {
		return sourceSession{}, errors.Join(err, release())
	}
	if private && !cfg.AllowPrivateURLs {
		return sourceSession{}, errors.Join(errors.New("cloud CDP endpoint targets a private network; allow_private_urls consent required"), release())
	}
	return sourceSession{endpoint: endpoint, release: release}, nil
}

func cloudRequest(ctx context.Context, method, endpoint string, headers http.Header, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = strings.NewReader(string(data))
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if e != nil {
		return e
	}
	req.Header = headers.Clone()
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return fmt.Errorf("cloud browser request failed (not retried; creation/action outcome may be unknown): %w", e)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloud browser returned HTTP %d; check provider key, plan and session quota", resp.StatusCode)
	}
	if out == nil {
		_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return e
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
