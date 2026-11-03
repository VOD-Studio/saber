package matrixplatform_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	platformmatrix "rua.plus/saber/internal/platform/matrix"
)

// fakeSession 记录平台对会话调用的动作，并用 channel 表达同步循环的进出。
type fakeSession struct {
	mu          sync.Mutex
	registers   int
	onlines     int
	autoJoins   int
	registerErr error

	started  chan struct{}
	finished chan struct{}
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		started:  make(chan struct{}, 4),
		finished: make(chan struct{}, 4),
	}
}

func (f *fakeSession) Register(events *matrix.EventHandler) error {
	f.mu.Lock()
	f.registers++
	err := f.registerErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if events == nil {
		return errors.New("测试替身收到空事件处理器")
	}
	return nil
}

func (f *fakeSession) Online() error {
	f.mu.Lock()
	f.onlines++
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) AutoJoin() {
	f.mu.Lock()
	f.autoJoins++
	f.mu.Unlock()
}

func (f *fakeSession) Sync(ctx context.Context) error {
	f.started <- struct{}{}
	<-ctx.Done()
	f.finished <- struct{}{}
	return ctx.Err()
}

func (f *fakeSession) counts() (registers, onlines, autoJoins int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registers, f.onlines, f.autoJoins
}

// newTestPlatform 用测试替身组装平台；命令服务存在即可通过 Start 的前置校验。
func newTestPlatform(t *testing.T, session platformmatrix.Session) *platformmatrix.Platform {
	t.Helper()
	client, err := mautrix.NewClient("http://unused.invalid", "@bot:local", "token")
	if err != nil {
		t.Fatal(err)
	}
	commands := matrix.NewCommandService(client, "@bot:local", nil)
	return platformmatrix.New(config.DefaultConfig(), commands, nil,
		platformmatrix.WithSession(session),
		platformmatrix.WithEventHandler(matrix.NewEventHandler(commands, 2)),
	)
}

// TestPlatform_StartStopLifecycle 验证 Start 只注册并启动一次同步，Stop 取消并等待退出。
func TestPlatform_StartStopLifecycle(t *testing.T) {
	session := newFakeSession()
	p := newTestPlatform(t, session)

	if err := p.Start(context.Background(), nil); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("Start 没有启动同步循环")
	}
	if registers, onlines, autoJoins := session.counts(); registers != 1 || onlines != 1 || autoJoins != 1 {
		t.Fatalf("会话动作次数错误: register=%d online=%d autojoin=%d", registers, onlines, autoJoins)
	}

	// 重复 Start 必须幂等：不能再注册一次，也不能起第二份同步。
	if err := p.Start(context.Background(), nil); err != nil {
		t.Fatalf("重复 Start 应返回 nil，实际 %v", err)
	}
	select {
	case <-session.started:
		t.Fatal("重复 Start 启动了第二份同步")
	case <-time.After(50 * time.Millisecond):
	}
	if registers, _, _ := session.counts(); registers != 1 {
		t.Fatalf("重复 Start 重复注册了事件处理器: %d 次", registers)
	}

	p.Stop()
	select {
	case <-session.finished:
	case <-time.After(time.Second):
		t.Fatal("Stop 没有取消同步循环")
	}

	// 重复 Stop 必须立即返回。
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

// TestPlatform_StopWithoutStart 验证未启动时的 Stop 是空操作。
func TestPlatform_StopWithoutStart(t *testing.T) {
	session := newFakeSession()
	p := newTestPlatform(t, session)
	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("未启动时 Stop 阻塞")
	}
	if registers, onlines, autoJoins := session.counts(); registers != 0 || onlines != 0 || autoJoins != 0 {
		t.Fatalf("未启动却调用了会话动作: register=%d online=%d autojoin=%d", registers, onlines, autoJoins)
	}
}

// TestPlatform_StartRequiresEventHandler 验证注入会话时缺少事件处理器会拒绝启动。
func TestPlatform_StartRequiresEventHandler(t *testing.T) {
	client, err := mautrix.NewClient("http://unused.invalid", "@bot:local", "token")
	if err != nil {
		t.Fatal(err)
	}
	commands := matrix.NewCommandService(client, "@bot:local", nil)
	p := platformmatrix.New(config.DefaultConfig(), commands, nil, platformmatrix.WithSession(newFakeSession()))
	if err := p.Start(context.Background(), nil); err == nil {
		t.Fatal("缺少事件处理器时应拒绝启动")
	}
	p.Stop()
}

// TestPlatform_StartRegisterError 验证事件注册失败会中止启动且保持可重复 Stop。
func TestPlatform_StartRegisterError(t *testing.T) {
	session := newFakeSession()
	session.registerErr = errors.New("注册失败")
	p := newTestPlatform(t, session)
	if err := p.Start(context.Background(), nil); err == nil {
		t.Fatal("事件注册失败时应返回错误")
	}
	select {
	case <-session.started:
		t.Fatal("注册失败后仍启动了同步")
	case <-time.After(50 * time.Millisecond):
	}
	p.Stop()
}

// TestNewSession_RequiresDependencies 验证真实会话在缺少客户端时明确报错而不是空转。
func TestNewSession_RequiresDependencies(t *testing.T) {
	session := platformmatrix.NewSession(nil, []string{"!room:local"})
	if err := session.Register(matrix.NewEventHandler(nil, 1)); err == nil {
		t.Fatal("缺少客户端时 Register 应报错")
	}
	if err := session.Sync(context.Background()); err == nil {
		t.Fatal("缺少客户端时 Sync 应报错")
	}
	session.AutoJoin() // 不应 panic
}

// TestNewSession_RegisterRequiresDefaultSyncer 验证非 DefaultSyncer 无法挂载事件回调。
func TestNewSession_RegisterRequiresDefaultSyncer(t *testing.T) {
	cfg := &config.MatrixConfig{Homeserver: "http://localhost:1", UserID: "@bot:local", AccessToken: "token"}
	client, err := matrix.NewMatrixClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	events := matrix.NewEventHandler(nil, 1)
	session := platformmatrix.NewSession(client, nil)
	if err := session.Register(events); err != nil {
		t.Fatalf("默认同步器应可注册: %v", err)
	}

	client.GetClient().Syncer = stubSyncer{}
	if err := session.Register(events); err == nil {
		t.Fatal("自定义同步器应被拒绝")
	}
}

// stubSyncer 是不支持回调注册的同步器，用来覆盖拒绝分支。
type stubSyncer struct{}

func (stubSyncer) ProcessResponse(context.Context, *mautrix.RespSync, string) error { return nil }
func (stubSyncer) OnFailedSync(*mautrix.RespSync, error) (time.Duration, error)     { return 0, nil }
func (stubSyncer) GetFilterJSON(id.UserID) *mautrix.Filter                          { return nil }
