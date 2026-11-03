// Package terminalplatform 把本机 HTTP 会话接口（saber chat / TUI 的后端）包装成平台接入端。
//
// 启动时创建本机令牌并监听回环地址，关闭时停止监听并等待服务 goroutine 退出；
// 是否监听完全由 platforms.terminal.enabled 决定，关闭后 saber chat 会连不上并有明确报错。
package terminalplatform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/platform"
	"rua.plus/saber/internal/server"
)

// readHeaderTimeout 限制请求头读取时间，避免慢连接长期占住本机入口。
const readHeaderTimeout = 5 * time.Second

// Platform 是本机 HTTP 聊天入口。
type Platform struct {
	listen    string
	tokenPath string
	service   *ai.Service
	// commands 延迟到 Start 才解析：命令注册表在平台登记之后才装配完成。
	commands func() server.CommandDispatcher
	// prebound 是装配层提前绑定的监听（用于端口占用时快速失败）；Start 后由平台接管。
	prebound net.Listener

	// mu 保护运行状态，使重复 Start/Stop 都幂等。
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	listener net.Listener
	server   *http.Server
}

// Option 定制终端平台的启动方式。
type Option func(*Platform)

// WithListener 注入已绑定的监听，Start 不再自行 listen。
//
// 装配层在初始化服务之前先用它占住端口，让端口冲突在创建数据库等副作用之前失败。
func WithListener(listener net.Listener) Option {
	return func(p *Platform) { p.prebound = listener }
}

// New 绑定本机入口的地址、令牌路径与共享服务。
//
// tokenPath 为空时使用默认令牌文件名，service 可为 nil（此时只提供健康与能力查询）。
func New(listen, tokenPath string, service *ai.Service, commands func() server.CommandDispatcher, opts ...Option) *Platform {
	p := &Platform{listen: listen, tokenPath: tokenPath, service: service, commands: commands}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return "terminal" }

// Start 创建令牌并在后台启动 HTTP 服务，不阻塞调用方。
//
// 本机入站请求直接经 ai.Service 与命令注册表处理，handler 参数只为满足平台接口，
// 这里不使用它。重复调用幂等；监听或令牌初始化失败会同步返回错误。
func (p *Platform) Start(ctx context.Context, _ chat.Handler) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		return nil
	}
	if p.listen == "" && p.prebound == nil {
		return errors.New("terminal 平台缺少监听地址")
	}

	// 令牌只在真正启动时生成：关闭终端平台不应在本机留下密钥文件。
	token, err := server.Token(p.tokenPath, true)
	if err != nil {
		return fmt.Errorf("初始化本机服务令牌失败: %w", err)
	}

	listener := p.prebound
	if listener == nil {
		listener, err = net.Listen("tcp", p.listen)
		if err != nil {
			return fmt.Errorf("无法启动服务（可能已运行）: %w", err)
		}
	}
	p.prebound = nil

	srv := &http.Server{
		Addr:              p.listen,
		Handler:           server.New(p.service, token, p.dispatcher()),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	p.cancel, p.done, p.listener, p.server = cancel, done, listener, srv

	go func() {
		defer close(done)
		if err := server.Serve(runCtx, srv, listener); err != nil {
			slog.Error("本机终端入口退出", "error", err)
		}
	}()

	slog.Info("本机终端入口已就绪", "listen", listener.Addr().String(), "chat", "saber chat")
	return nil
}

// Stop 停止监听并等待服务 goroutine 退出；未启动或重复调用都是空操作。
func (p *Platform) Stop() {
	p.mu.Lock()
	cancel, done, srv := p.cancel, p.done, p.server
	p.cancel, p.done, p.server, p.listener = nil, nil, nil, nil
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if srv != nil {
		if err := srv.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("关闭本机终端入口失败", "error", err)
		}
	}
	if done != nil {
		<-done
	}
}

// dispatcher 在启动时解析命令分发器；未注入时本机命令入口保持关闭。
func (p *Platform) dispatcher() server.CommandDispatcher {
	if p.commands == nil {
		return nil
	}
	return p.commands()
}

// Addr 返回当前监听地址；未启动时为空。
func (p *Platform) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

// 确保实现满足平台接口。
var _ platform.Platform = (*Platform)(nil)
