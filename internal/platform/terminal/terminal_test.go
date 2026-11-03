package terminalplatform_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	terminalplatform "rua.plus/saber/internal/platform/terminal"
)

// eventually 轮询条件，避免用固定 sleep 等待监听就绪。
func eventually(t *testing.T, check func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

// getInfo 带令牌请求本机入口的健康信息。
func getInfo(t *testing.T, addr, token string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}

// TestPlatform_StartStopLifecycle 验证启动后监听可用、令牌落盘，Stop 后连接被关闭。
func TestPlatform_StartStopLifecycle(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), ".saber-token")
	p := terminalplatform.New("127.0.0.1:0", tokenPath, nil, nil)

	if err := p.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	addr := p.Addr()
	if addr == "" {
		t.Fatal("Start 之后应已监听")
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("Start 应创建令牌文件: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("令牌文件权限应为 0600，实际 %o", perm)
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}

	// 重复 Start 幂等：地址不变。
	if err := p.Start(context.Background(), nil); err != nil {
		t.Fatalf("重复 Start 应返回 nil，实际 %v", err)
	}
	if p.Addr() != addr {
		t.Fatalf("重复 Start 换了监听地址: %s -> %s", addr, p.Addr())
	}

	if status, body := getInfo(t, addr, string(token[:len(token)-1])); status != http.StatusOK {
		t.Fatalf("带令牌请求应成功，实际 %d: %s", status, body)
	}
	if status, _ := getInfo(t, addr, ""); status != http.StatusUnauthorized {
		t.Fatalf("无令牌请求应返回 401，实际 %d", status)
	}

	p.Stop()
	if p.Addr() != "" {
		t.Fatal("Stop 之后不应再报告监听地址")
	}
	if response, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + "/v1/info"); err == nil {
		_ = response.Body.Close()
		t.Fatal("Stop 之后连接应被关闭")
	}

	// 重复 Stop 立即返回。
	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("重复 Stop 阻塞")
	}
}

// TestPlatform_StartWithPreboundListener 验证装配层提前占住的监听会被平台接管并关闭。
func TestPlatform_StartWithPreboundListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	want := listener.Addr().String()
	tokenPath := filepath.Join(t.TempDir(), ".saber-token")
	p := terminalplatform.New("", tokenPath, nil, nil, terminalplatform.WithListener(listener))

	if err := p.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if p.Addr() != want {
		t.Fatalf("应复用预绑定监听 %s，实际 %s", want, p.Addr())
	}
	eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", want, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, "预绑定监听未开始服务")

	p.Stop()
	eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", want, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, "Stop 之后监听仍可连接")
}

// TestPlatform_StartWithoutAddress 验证缺少监听地址时明确报错。
func TestPlatform_StartWithoutAddress(t *testing.T) {
	p := terminalplatform.New("", filepath.Join(t.TempDir(), ".saber-token"), nil, nil)
	if err := p.Start(context.Background(), nil); err == nil {
		t.Fatal("缺少监听地址时应报错")
	}
	p.Stop()
}

// TestPlatform_StartCancelledContext 验证父 context 取消等价于 Stop。
func TestPlatform_StartCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := terminalplatform.New("127.0.0.1:0", filepath.Join(t.TempDir(), ".saber-token"), nil, nil)
	if err := p.Start(ctx, nil); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	addr := p.Addr()
	cancel()
	eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, "父 context 取消后监听仍在")
	p.Stop()
}
