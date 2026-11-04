// Package matrixplatform 是 Matrix 聊天平台接入端：持有 Matrix 账号与出站 adapter，
// 并接管会话生命周期（事件注册、在线状态、自动进房与同步循环）。
package matrixplatform

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"maunium.net/go/mautrix"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/platform"
)

// Platform 以 bot 身份接入 Matrix 房间与私聊。
//
// 登录与 E2EE 初始化属于装配层（需要口令、密钥路径等凭据），装配完成后把
// Session 与 EventHandler 注入本平台；Start 负责注册事件并启动同步，Stop 取消并等待退出。
type Platform struct {
	cfg          *config.Config
	commands     *matrix.CommandService
	media        *matrix.MediaService
	avatarClient *mautrix.Client

	session Session
	events  *matrix.EventHandler

	// mu 保护 cancel/done，使重复 Start 与并发 Stop 都有确定语义。
	mu sync.Mutex
	// cancel 非空表示平台已启动。
	cancel context.CancelFunc
	// done 在同步 goroutine 退出时关闭；同步模式下 Stop 等待它。
	done chan struct{}
	// registered 记录事件回调是否已挂到同步器，避免重启时重复注册同一个处理器。
	registered bool
}

// Option 定制平台运行时依赖。
type Option func(*Platform)

// WithSession 注入 Matrix 会话运行时。未注入时平台只提供出站 adapter，
// 不会注册事件或启动同步（供只装配 adapter 的场景使用）。
func WithSession(session Session) Option {
	return func(p *Platform) { p.session = session }
}

// WithEventHandler 注入装配好的事件处理器；注入会话时必填，Start 时注册到同步器。
func WithEventHandler(events *matrix.EventHandler) Option {
	return func(p *Platform) { p.events = events }
}

// New 绑定 Matrix 账号与共享聊天链路。
func New(cfg *config.Config, commands *matrix.CommandService, media *matrix.MediaService, opts ...Option) *Platform {
	p := &Platform{cfg: cfg, commands: commands, media: media}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return "matrix" }

// Start 接管 Matrix 会话：注册事件回调、上报在线状态、自动进房，然后在后台跑同步循环。
//
// 重复调用是幂等的：已在运行时直接返回 nil，不会留下第二份同步循环。
// 会话或事件注册失败属于「重试也没用」的装配问题，同步返回错误。
func (p *Platform) Start(ctx context.Context, _ chat.Handler) error {
	if p.commands == nil {
		return errors.New("matrix 平台缺少命令服务，无法启动")
	}
	if p.session != nil && p.events == nil {
		return errors.New("matrix 平台缺少事件处理器，无法启动")
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		slog.Debug("matrix 平台已在运行，忽略重复启动")
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	if p.session == nil {
		// 无会话时只登记已启动，Stop 仍然幂等可调用。
		p.cancel = cancel
		p.done = closedSignal()
		return nil
	}

	if !p.registered {
		if err := p.session.Register(p.events); err != nil {
			cancel()
			return err
		}
		p.registered = true
	}
	if err := p.session.Online(); err != nil {
		// 在线状态失败不影响收消息，保持与旧装配层一致的降级行为。
		slog.Warn("Failed to set presence", "error", err)
	}
	p.session.AutoJoin()

	done := make(chan struct{})
	p.cancel = cancel
	p.done = done
	go func() {
		defer close(done)
		if err := p.session.Sync(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Matrix sync 退出", "error", err)
		}
	}()
	return nil
}

// Stop 取消同步并等待 goroutine 退出；未启动或重复调用都是空操作。
//
// 事件回调只在第一次 Start 注册：重启时同步器上的处理器列表不应被重复追加。
func (p *Platform) Stop() {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.cancel, p.done = nil, nil
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

// ChatAdapter 返回带流式编辑与媒体解析的 adapter，用于展示入站命令的回复。
func (p *Platform) ChatAdapter(handler chat.Handler) *matrix.ChatAdapter {
	return matrix.NewChatAdapter(p.commands, p.media, p.cfg.Matrix.Media, p.cfg.Matrix.StreamEdit.Enabled, handler)
}

// DeliveryAdapter 返回任务投递 adapter，是否原地编辑由 Matrix 展示开关决定。
func (p *Platform) DeliveryAdapter() chat.Adapter {
	return matrix.NewChatAdapter(p.commands, p.media, p.cfg.Matrix.Media, p.cfg.Matrix.StreamEdit.Enabled, nil)
}

// closedSignal 返回已关闭的 channel，用于无 goroutine 的「已启动」状态。
func closedSignal() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// 确保实现满足平台接口与可选的任务投递端口。
var (
	_ platform.Platform     = (*Platform)(nil)
	_ platform.TaskDelivery = (*Platform)(nil)
)
