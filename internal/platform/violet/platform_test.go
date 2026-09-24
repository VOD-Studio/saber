package violetplatform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/conversation"
	"rua.plus/saber/internal/platform"
)

// startTestPlatform 按生产路径构造并启动接入端，测试结束时统一收尾。
func startTestPlatform(t *testing.T, fake *fakeViolet, handler *recordingHandler) *Platform {
	t.Helper()
	platform := New(newTestConfig(fake.endpoint()))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		platform.Stop()
		cancel()
	})
	if err := platform.Start(ctx, handler.handle); err != nil {
		t.Fatalf("启动 violet 平台失败: %v", err)
	}
	return platform
}

// deliver 把消息同时写进历史与事件流，等价于「这条消息既被推送过也可能被补拉到」。
func (f *fakeViolet) deliver(t *testing.T, conversation, sender, senderName, content string, at time.Time) messageDTO {
	t.Helper()
	message := f.pushMessage(conversation, sender, senderName, content, at)
	f.pushEvent(message)
	return message
}

// TestPlatform_Name 验证平台标识与配置键、会话字段同源。
func TestPlatform_Name(t *testing.T) {
	t.Parallel()
	if platformName != "violet" {
		t.Fatalf("平台标识常量 = %q, want violet", platformName)
	}
	if got := New(nil).Name(); got != platformName {
		t.Fatalf("Name = %q, want %q", got, platformName)
	}
}

// TestPlatform_DeliveryAdapterBeforeStart 验证任务投递器在 Start 之前就能注册：
// bot.go 的装配顺序是先注册平台与投递器、后 Start。
func TestPlatform_DeliveryAdapterBeforeStart(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	platform := New(newTestConfig(fake.endpoint()))
	if platform.DeliveryAdapter() == nil {
		t.Fatal("未启动也必须能注册任务投递")
	}
	if New(nil).DeliveryAdapter() != nil {
		t.Fatal("没有配置时不该给出可用的投递器")
	}
}

// TestPlatform_StartRejectsBadConfig 验证「怎么重试都不会好」的配置问题在 Start 同步报错。
func TestPlatform_StartRejectsBadConfig(t *testing.T) {
	t.Parallel()
	handler := newRecordingHandler()
	ctx := context.Background()

	missing := newTestConfig("https://blog.example.com")
	missing.Platforms.Violet.BotToken = ""
	if err := New(missing).Start(ctx, handler.handle); err == nil {
		t.Fatal("缺少 bot_token 应启动失败")
	}
	if err := New(nil).Start(ctx, handler.handle); err == nil {
		t.Fatal("没有配置应启动失败")
	}
	fake := newFakeViolet(t)
	if err := New(newTestConfig(fake.endpoint())).Start(ctx, nil); err == nil {
		t.Fatal("缺少消息处理入口应启动失败")
	}
}

// TestPlatform_WrongTokenKeepsRetrying 验证凭据错误只让重连退避，不会把平台启动拖崩。
func TestPlatform_WrongTokenKeepsRetrying(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	cfg := newTestConfig(fake.endpoint())
	cfg.Platforms.Violet.BotToken = "violet_bot_wrong"
	platform := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel() })
	handler := newRecordingHandler()
	if err := platform.Start(ctx, handler.handle); err != nil {
		t.Fatalf("鉴权失败属于运行期问题，不该让启动报错: %v", err)
	}
	platform.Stop()
}

// TestPlatform_HandlerFailureReturnsCard 验证聊天链路在创建任务前失败时仍回传一张失败卡片。
func TestPlatform_HandlerFailureReturnsCard(t *testing.T) {
	fake := newFakeViolet(t)
	p := New(newTestConfig(fake.endpoint()))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { p.Stop(); cancel() })
	if err := p.Start(ctx, func(context.Context, chat.Message, chat.Adapter) (agent.Result, error) {
		return agent.Result{}, errors.New("模型不可用")
	}); err != nil {
		t.Fatal(err)
	}
	fake.deliver(t, testDirectRoom, "user-1", "alice", "你好", time.Now())
	deadline := time.After(2 * time.Second)
	for {
		fake.mu.Lock()
		var reply messageDTO
		for _, message := range fake.history[testDirectRoom] {
			if message.Sender.ID == testBotUserID {
				reply = message
				break
			}
		}
		fake.mu.Unlock()
		if reply.BotReply != nil && reply.BotReply.Status == "failed" && strings.Contains(reply.Content, "模型不可用") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("未返回失败卡片: %+v", reply)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestPlatform_ModelFailureDoesNotSendSecondCard(t *testing.T) {
	fake := newFakeViolet(t)
	p := New(newTestConfig(fake.endpoint()))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { p.Stop(); cancel() })
	done := make(chan struct{})
	if err := p.Start(ctx, func(ctx context.Context, message chat.Message, adapter chat.Adapter) (agent.Result, error) {
		defer close(done)
		return conversation.Deliver(ctx, func(_ context.Context, _ agent.Request, emit func(agent.Event)) (agent.Result, error) {
			emit(agent.Event{Kind: agent.TextDelta, Text: "部分正文"})
			return agent.Result{Status: agent.Failed}, errors.New("模型断流")
		}, agent.Request{}, message, adapter, chat.Display{}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	fake.deliver(t, testDirectRoom, "user-1", "alice", "你好", time.Now())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("模型处理未结束")
	}
	time.Sleep(20 * time.Millisecond) // 让调用方完成错误分支，核对它没有补发第二张卡片。
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sent) != 1 || len(fake.edits) == 0 {
		t.Fatalf("失败回复重复发送: sent=%+v edits=%+v", fake.sent, fake.edits)
	}
	final := fake.history[testDirectRoom][0]
	if final.BotReply == nil || final.BotReply.Status != "failed" || !strings.Contains(final.Content, "模型断流") || !strings.Contains(final.Content, "部分正文") {
		t.Fatalf("模型失败卡片 = %+v", final)
	}
}

// TestPlatform_DirectMessageAnswers 验证私聊消息被规范化后交给共享聊天链路。
func TestPlatform_DirectMessageAnswers(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	now := time.Now()
	fake.deliver(t, testDirectRoom, "user-1", "alice", "@(saber:11111111-1111-1111-1111-111111111111) 讲个笑话", now)
	fake.deliver(t, testDirectRoom, "user-1", "alice", "!ai 今天天气如何", now.Add(time.Second))

	// 并发处理不保证跨消息顺序，因此按内容取回各自那条。
	mentioned := handler.waitForText(t, "@saber 讲个笑话")
	if mentioned.Session.Platform != platformName || mentioned.Session.Conversation != testDirectRoom {
		t.Fatalf("会话作用域 = %+v", mentioned.Session)
	}
	if mentioned.Session.Account != fake.endpointHost() {
		t.Fatalf("账号标识 = %q, want 站点主机名 %q", mentioned.Session.Account, fake.endpointHost())
	}
	if mentioned.SenderID != "user-1" || mentioned.ID == "" {
		t.Fatalf("发送者与消息标识未透传: %+v", mentioned)
	}
	if mentioned.ControlText != nil {
		t.Fatal("Violet 没有引用回退包装，ControlText 应留空由核心按 Text 识别命令")
	}
	if !strings.Contains(mentioned.Text, "讲个笑话") {
		t.Fatalf("提及 token 未剥离: %q", mentioned.Text)
	}
	handler.waitForText(t, "!ai 今天天气如何") // 共享命令入口负责解析，平台保留原文。
}

// TestPlatform_IgnoredInbound 验证自回声、非文本、缺发送者、被删消息与
// 剥离后为空的消息都不会惊动模型。
func TestPlatform_IgnoredInbound(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	now := time.Now()
	fake.deliver(t, testDirectRoom, testBotUserID, testBotUsername, "我自己的回声", now)
	fake.deliver(t, testDirectRoom, "user-1", "alice", "@(saber:u) ", now.Add(time.Second)) // 只 @ 不提问
	image := fake.pushMessage(testDirectRoom, "user-1", "alice", "", now.Add(2*time.Second))
	image.Type = "image"
	fake.pushEvent(image)
	deleted := fake.pushMessage(testDirectRoom, "user-1", "alice", "已撤回", now.Add(3*time.Second))
	deleted.IsDeleted = true
	fake.pushEvent(deleted)
	anonymous := fake.pushMessage(testDirectRoom, "", "", "没有作者", now.Add(4*time.Second))
	fake.pushEvent(anonymous)
	waitForIdle(t, 400*time.Millisecond)
	if got := handler.messages(); len(got) != 0 {
		t.Fatalf("不该回答的消息被投递了: %+v", got)
	}
}

// TestPlatform_GroupMentionGate 验证群聊只在点名本 bot 时回答。
func TestPlatform_GroupMentionGate(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	now := time.Now()
	fake.deliver(t, testGroupRoom, "user-2", "bob", "群里闲聊不必插话", now)
	fake.deliver(t, testGroupRoom, "user-2", "bob", "@(saber:6f1e) 帮忙看看这个报错", now.Add(time.Second))

	message := handler.waitForMessage(t, 0)
	if message.Session.Conversation != testGroupRoom {
		t.Fatalf("被 @ 的群聊消息未落到该会话: %+v", message)
	}
	waitForIdle(t, 300*time.Millisecond)
	if got := len(handler.messages()); got != 1 {
		t.Fatalf("群聊回答条数 = %d, want 1（未被 @ 的那条不该回答）", got)
	}
}

func TestPlatform_GroupCommandNeedsExactLeadingTarget(t *testing.T) {
	fake := newFakeViolet(t)
	cfg := newTestConfig(fake.endpoint())
	cfg.Platforms.Violet.DirectChatAutoReply = false
	cfg.Platforms.Violet.GroupChatMentionReply = false
	p := New(cfg)
	p.setIdentity(fake.profile)
	for _, tc := range []struct {
		name, conversation, content, want string
		accept                            bool
	}{
		{"self", testGroupRoom, "@(saber:bot-user-1) /task status @(other:other-id)", "/task status @(other:other-id)", true},
		{"other_then_self_argument", testGroupRoom, "@(other:other-id) /task status @(saber:bot-user-1)", "", false},
		{"bare_group", testGroupRoom, "/task list @(saber:bot-user-1)", "", false},
		{"spoofed_name", testGroupRoom, "@(saber:other-id) /task list @(saber:bot-user-1)", "", false},
		{"direct", testDirectRoom, "/task list", "/task list", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := inbound{conversationID: tc.conversation, message: messageDTO{ID: tc.name, Sender: userDTO{ID: "user"}, Type: "text", Content: tc.content}}
			message, ok := p.normalizeMessage(context.Background(), item)
			if ok != tc.accept || ok && message.Text != tc.want {
				t.Fatalf("normalizeMessage = %q,%v，期望 %q,%v", message.Text, ok, tc.want, tc.accept)
			}
		})
	}
}

func TestPlatform_GroupEscapedSlashStaysOrdinaryText(t *testing.T) {
	fake := newFakeViolet(t)
	p := New(newTestConfig(fake.endpoint()))
	p.setIdentity(fake.profile)
	item := inbound{conversationID: testGroupRoom, message: messageDTO{ID: "escaped", Sender: userDTO{ID: "user"}, Type: "text", Content: "@(saber:bot-user-1) //task list"}}
	message, ok := p.normalizeMessage(context.Background(), item)
	if !ok || message.Text != "//task list" {
		t.Fatalf("转义消息 = %q,%v", message.Text, ok)
	}
}

// TestPlatform_ConfigGates 验证两个自动回复开关各自生效。
func TestPlatform_ConfigGates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		configure    func(*config.VioletConfig)
		conversation string
		content      string
	}{
		{
			name:         "关闭私聊自动回复",
			configure:    func(v *config.VioletConfig) { v.DirectChatAutoReply = false },
			conversation: testDirectRoom,
			content:      "在吗",
		},
		{
			name:         "关闭群聊提及回复",
			configure:    func(v *config.VioletConfig) { v.GroupChatMentionReply = false },
			conversation: testGroupRoom,
			content:      "@(saber:6f1e) 在吗",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeViolet(t)
			cfg := newTestConfig(fake.endpoint())
			tc.configure(&cfg.Platforms.Violet)
			platform := New(cfg)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() { platform.Stop(); cancel() })
			handler := newRecordingHandler()
			if err := platform.Start(ctx, handler.handle); err != nil {
				t.Fatal(err)
			}
			fake.deliver(t, tc.conversation, "user-1", "alice", tc.content, time.Now())
			waitForIdle(t, 400*time.Millisecond)
			if got := handler.messages(); len(got) != 0 {
				t.Fatalf("开关已关闭仍然回答了: %+v", got)
			}
		})
	}
}

// TestPlatform_UnknownKindIsStrict 验证会话形态查不到时按群聊从严：
// 宁可漏答一条私聊，也不要在没被 @ 的房间里插话。
func TestPlatform_UnknownKindIsStrict(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	fake.setKindFailure(true)
	now := time.Now()
	fake.deliver(t, "conv-ghost", "user-1", "alice", "形态未知的会话里闲聊", now)
	fake.deliver(t, "conv-ghost", "user-1", "alice", "@(saber:6f1e) 形态未知但点名了", now.Add(time.Second))

	message := handler.waitForText(t, "@saber 形态未知但点名了")
	if message.Session.Conversation != "conv-ghost" {
		t.Fatalf("被点名的消息未落到该会话: %+v", message)
	}
	waitForIdle(t, 300*time.Millisecond)
	if got := len(handler.messages()); got != 1 {
		t.Fatalf("回答条数 = %d, want 1", got)
	}
}

// TestPlatform_KindIsCached 验证形态只查一次并复用缓存。
func TestPlatform_KindIsCached(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	fake.resetConversationCalls()
	now := time.Now()
	for index := 0; index < 3; index++ {
		fake.deliver(t, testDirectRoom, "user-1", "alice", "追问一句", now.Add(time.Duration(index)*time.Second))
		handler.waitForMessage(t, index)
	}
	if got := fake.getConversationCalls(); got > 0 {
		t.Fatalf("已有缓存仍然每条消息查一次形态: %d 次", got)
	}
}

// TestPlatform_FirstConnectDoesNotReplayHistory 验证首次连接只打基线：
// 站内历史不是新问题，回放一遍等于机器人自问自答。
func TestPlatform_FirstConnectDoesNotReplayHistory(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	base := time.Now().Add(-time.Hour)
	for index := 0; index < 3; index++ {
		fake.pushMessage(testDirectRoom, "user-1", "alice", "很久以前的消息", base.Add(time.Duration(index)*time.Minute))
	}
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	waitForIdle(t, 500*time.Millisecond)
	if got := handler.messages(); len(got) != 0 {
		t.Fatalf("启动时回放了历史: %+v", got)
	}
	fake.deliver(t, testDirectRoom, "user-1", "alice", "刚刚问的问题", time.Now())
	handler.waitForText(t, "刚刚问的问题")
}

// TestPlatform_ReconnectBackfillsOnceInOrder 验证断线期间的消息被补回、
// 已处理过的消息不被重放，且补拉按时间正序交给模型。
func TestPlatform_ReconnectBackfillsOnceInOrder(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	now := time.Now()
	fake.deliver(t, testDirectRoom, "user-1", "alice", "断线前的问题", now)
	handler.waitForMessage(t, 0)

	fake.closeEvents() // 模拟连接被掐断：这两条只能靠补拉回来，没有事件可推
	fake.pushMessage(testDirectRoom, "user-1", "alice", "断线期间的第一个问题", now.Add(2*time.Second))
	fake.pushMessage(testDirectRoom, "user-1", "alice", "断线期间的第二个问题", now.Add(3*time.Second))
	fake.restartEvents()

	// 补拉的时序由 pullSince 与反转入队保证（见 TestPlatform_PullSinceOrder），
	// 这里只断言「一条不漏、一条不重」：并发 worker 下跨消息顺序本就不是承诺。
	handler.waitForText(t, "断线期间的第一个问题")
	handler.waitForText(t, "断线期间的第二个问题")
	waitForIdle(t, 500*time.Millisecond)
	if got := handler.messages(); len(got) != 3 {
		t.Fatalf("消息条数 = %d, want 3（去重应挡住重放）: %+v", len(got), texts(got))
	}
}

// TestPlatform_StopIsIdempotent 验证未启动与重复停止都不 panic，且 Stop 会等在途处理收尾。
func TestPlatform_StopIsIdempotent(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	platform := New(newTestConfig(fake.endpoint()))
	platform.Stop()
	handler := newRecordingHandler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := platform.Start(ctx, handler.handle); err != nil {
		t.Fatal(err)
	}
	if err := platform.Start(ctx, handler.handle); err == nil {
		t.Fatal("重复 Start 必须被拒绝，否则会留下一组永不退出的 goroutine")
	}
	platform.Stop()
	platform.Stop()
	if platform.running() {
		t.Fatal("Stop 之后应报告未启动")
	}
	// 停止后允许再次启动：状态机不能因为一次 Stop 就锁死平台。
	if err := platform.Start(ctx, handler.handle); err != nil {
		t.Fatalf("停止后应可再次启动: %v", err)
	}
	platform.Stop()
}

// TestPlatform_PullSinceOrder 验证补拉只取晚于水位的消息，且保持服务端给的新→旧顺序
// （调用方靠反转得到时间正序，乱序交给模型会让回答与提问对不上）。
func TestPlatform_PullSinceOrder(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	platform := New(newTestConfig(fake.endpoint()))
	base := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 4; index++ {
		fake.pushMessage(testDirectRoom, "user-1", "alice", fmt.Sprintf("第 %d 条", index), base.Add(time.Duration(index)*time.Minute))
	}
	// 水位落在第 2 条之后：应只回第 3、4 两条（含与水位同刻的那条，靠去重兜重复）。
	messages := platform.pullSince(context.Background(), testDirectRoom, base.Add(2*time.Minute))
	if len(messages) != 2 {
		t.Fatalf("补拉条数 = %d, want 2: %+v", len(messages), ids(messages))
	}
	if messages[0].ID != fake.lastMessageID(testDirectRoom) {
		t.Fatalf("首位应是最新消息: %+v", ids(messages))
	}
}

// ids 提取消息 ID，便于断言失败时读出顺序。
func ids(messages []messageDTO) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.ID)
	}
	return out
}

// TestPlatform_CommandPrefixesReachSharedHandler 验证平台原样传递新旧命令前缀。
func TestPlatform_CommandPrefixesReachSharedHandler(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	now := time.Now()
	fake.deliver(t, testDirectRoom, "user-1", "alice", "!ai 帮我看这段日志", now)
	fake.deliver(t, testDirectRoom, "user-1", "alice", "!ai-switch gpt", now.Add(time.Second))
	fake.deliver(t, testDirectRoom, "user-1", "alice", "!schedule list", now.Add(2*time.Second))
	handler.waitForText(t, "!ai 帮我看这段日志")
	handler.waitForText(t, "!ai-switch gpt")
	handler.waitForText(t, "!schedule list")
	waitForIdle(t, 400*time.Millisecond)
	if got := handler.messages(); len(got) != 3 {
		t.Fatalf("命令没有全部交给共享入口: %+v", texts(got))
	}
}

// TestDescribeReconnect 验证凭据被拒与暂时断流给出不同的日志：前者需要人动手改配置。
func TestDescribeReconnect(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		&apiError{Status: 401, Code: "UNAUTHORIZED"},
		&apiError{Status: 403, Code: "FORBIDDEN"},
	} {
		if got := describeReconnect(err); !strings.Contains(got, "bot_token") {
			t.Fatalf("状态 %d 的凭据错误应点名 bot_token，got %q", clientStatus(err), got)
		}
	}
	for _, err := range []error{
		&apiError{Status: 500, Code: "INTERNAL"},
		errors.New("connection refused"),
		io.ErrUnexpectedEOF,
	} {
		if got := describeReconnect(err); got != "violet 事件流中断，准备重连" {
			t.Fatalf("暂时断流不该被判成凭据问题，got %q", got)
		}
	}
}

// TestPlatform_SatisfiesRegistryContract 验证接入端可被注册表按配置挑选出来。
func TestPlatform_SatisfiesRegistryContract(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	registry := platform.NewRegistry()
	platformValue := New(newTestConfig(fake.endpoint()))
	registry.Register(platformValue)
	cfg := config.DefaultConfig()
	if got := registry.Enabled(cfg); len(got) != 0 {
		t.Fatalf("未启用的平台不该被选出: %+v", got)
	}
	cfg.Platforms.Violet.Enabled = true
	enabled := registry.Enabled(cfg)
	if len(enabled) != 1 || enabled[0] != platformValue {
		t.Fatalf("Enabled = %+v", enabled)
	}
}

// texts 提取消息正文，让断言失败信息可读。
func texts(messages []chat.Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.Text)
	}
	return out
}
