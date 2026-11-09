package desktop

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/config"
)

// Dashboard 在随机本机端口提供工作台；令牌仅由 Go 宿主转发，一次性登录后使用私有 cookie。
func Dashboard(ctx context.Context, cfg config.DesktopConfig, appDir string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.TokenFile == "" {
		return fmt.Errorf("dashboard 需要本机 desktop.token_file")
	}
	st, err := os.Stat(cfg.TokenFile)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("dashboard token_file 需要 0600")
	}
	raw, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return fmt.Errorf("令牌为空")
	}
	upstream, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return err
	}
	ip := net.ParseIP(upstream.Hostname())
	if upstream.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("dashboard 禁止转发本机 token 到远程")
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	login := hex.EncodeToString(nonce)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	cookie := hex.EncodeToString(nonce)
	deadline := time.Now().Add(time.Minute)
	var mu sync.Mutex
	used := false
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	origin := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: time.Hour, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("拒绝 API 重定向") }}
	client.Transport = eventCursorTransport{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bridge/request", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path   string          `json:"path"`
			Method string          `json:"method"`
			Body   json.RawMessage `json:"body"`
			Stream bool            `json:"stream"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 24<<20)).Decode(&input); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if !strings.HasPrefix(input.Path, "/v1/") || strings.Contains(input.Path, "..") || strings.ContainsAny(input.Path, "\\\r\n") || strings.Contains(strings.ToLower(input.Path), "%2f") || strings.Contains(strings.ToLower(input.Path), "%2e") || strings.Contains(strings.ToLower(input.Path), "%5c") {
			http.Error(w, "无效 API 路径", 400)
			return
		}
		if input.Method != "GET" && input.Method != "POST" && input.Method != "PATCH" && input.Method != "DELETE" {
			http.Error(w, "无效 method", 400)
			return
		}
		target := cfg.Endpoint + input.Path
		req, e := http.NewRequestWithContext(r.Context(), input.Method, target, strings.NewReader(string(input.Body)))
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, e := client.Do(req)
		if e != nil {
			http.Error(w, e.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = res.Body.Close() }()
		w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
		w.WriteHeader(res.StatusCode)
		if input.Stream {
			buf := make([]byte, 16<<10)
			for {
				n, e := res.Body.Read(buf)
				if n > 0 {
					if _, we := w.Write(buf[:n]); we != nil {
						return
					}
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
				if e != nil {
					return
				}
			}
		}
		_, _ = io.Copy(w, io.LimitReader(res.Body, 32<<20))
	})
	mux.HandleFunc("GET /bridge/audio", audioProxy(cfg.Endpoint, token, origin))
	mux.Handle("GET /", http.FileServer(http.Dir(filepath.Join(appDir, "ui"))))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != listener.Addr().String() {
			http.Error(w, "无效 Host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/" && r.URL.Query().Get("login") != "" {
			mu.Lock()
			valid := !used && time.Now().Before(deadline) && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("login")), []byte(login)) == 1
			if valid {
				used = true
			}
			mu.Unlock()
			if !valid {
				http.Error(w, "登录链接已失效", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "saber_dashboard", Value: cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		c, e := r.Cookie("saber_dashboard")
		if e != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(cookie)) != 1 {
			http.Error(w, "请使用 saber desktop dashboard 的一次性登录链接", http.StatusUnauthorized)
			return
		}
		if r.Method != "GET" && r.Header.Get("Origin") != origin {
			http.Error(w, "跨源请求被拒绝", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	_, _ = fmt.Fprintln(os.Stdout, "一次性本机登录（60 秒有效，请勿分享）: "+origin+"/?login="+login)
	err = srv.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

type eventCursorTransport struct{}

func (eventCursorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		r.Header.Set("Last-Event-ID", cursor)
	}
	return http.DefaultTransport.RoundTrip(r)
}
