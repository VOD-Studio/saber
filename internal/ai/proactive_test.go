//go:build goolm

package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

func TestNewProactiveManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *config.ProactiveConfig
		aiService      *Service
		roomService    ProactiveRooms
		globalAIConfig *config.AIConfig
		wantErr        bool
		errContains    string
	}{
		{
			name:           "空配置",
			config:         nil,
			aiService:      nil,
			roomService:    nil,
			globalAIConfig: nil,
			wantErr:        true,
			errContains:    "主动聊天配置不能为空",
		},
		{
			name:           "空 AI 服务",
			config:         &config.ProactiveConfig{},
			aiService:      nil,
			roomService:    nil,
			globalAIConfig: nil,
			wantErr:        true,
			errContains:    "AI 服务不能为空",
		},
		{
			name:           "空房间端口",
			config:         &config.ProactiveConfig{},
			aiService:      &Service{},
			roomService:    nil,
			globalAIConfig: nil,
			wantErr:        true,
			errContains:    "主动聊天房间端口不能为空",
		},
		{
			name:           "空全局 AI 配置",
			config:         &config.ProactiveConfig{},
			aiService:      &Service{},
			roomService:    &FakeProactiveRooms{},
			globalAIConfig: nil,
			wantErr:        true,
			errContains:    "全局 AI 配置不能为空",
		},
		{
			name: "有效配置",
			config: &config.ProactiveConfig{
				Enabled:            false,
				MaxMessagesPerDay:  5,
				MinIntervalMinutes: 60,
			},
			aiService:      &Service{},
			roomService:    &FakeProactiveRooms{},
			globalAIConfig: &config.AIConfig{},
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, err := NewProactiveManager(
				tt.config,
				tt.aiService,
				tt.roomService,
				nil,
				tt.globalAIConfig,
			)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewProactiveManager() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if tt.errContains != "" && err != nil {
					if !contains(err.Error(), tt.errContains) {
						t.Errorf("NewProactiveManager() error = %v, want error containing %q", err, tt.errContains)
					}
				}
			} else {
				if manager == nil {
					t.Errorf("NewProactiveManager() returned nil manager, want non-nil")
				}
			}
		})
	}
}

func TestProactiveManagerLifecycle(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 60,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     60,
			CheckIntervalMinutes: 15,
		},
		Schedule: config.ScheduleConfig{
			Enabled: true,
			Times:   []string{"09:00", "18:00"},
		},
		NewMember: config.NewMemberConfig{
			Enabled:       true,
			WelcomePrompt: "欢迎新成员",
		},
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()

	// 测试 Start
	manager.Start(ctx)

	// 测试 Stop
	manager.Stop()
}

func TestProactiveManagerDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled: false,
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()

	// Start 在禁用时应立即返回
	manager.Start(ctx)

	// Stop 不应该 panic
	manager.Stop()
}

func TestProactiveManagerShutdown(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 60,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     60,
			CheckIntervalMinutes: 15,
		},
		Schedule: config.ScheduleConfig{
			Enabled: true,
			Times:   []string{"09:00", "18:00"},
		},
		NewMember: config.NewMemberConfig{
			Enabled:       true,
			WelcomePrompt: "欢迎新成员",
		},
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()

	manager.Start(ctx)

	// Give it a moment to start
	time.Sleep(10 * time.Millisecond)

	// Stop should complete quickly without hanging
	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()

	// Wait for stop to complete or timeout
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out - possible goroutine leak")
	}
}

func TestProactiveManagerShutdownWithContext(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 60,
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	// 创建一个可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())

	manager.Start(ctx)

	// Give it a moment to start
	time.Sleep(10 * time.Millisecond)

	// Cancel the context
	cancel()

	// Stop should complete quickly
	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()

	// Wait for stop to complete or timeout
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out after context cancellation")
	}
}

// contains 检查字符串是否包含子串。
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || findSubstring(s, substr))
}

// findSubstring 辅助函数用于查找子串。
func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestOnNewMember_Disabled(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled: false,
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()
	roomID := TestRoomID(1)
	userID := TestUserID(1)

	err = manager.OnNewMember(ctx, roomID, userID)
	if err != nil {
		t.Errorf("OnNewMember() should return nil when disabled, got error: %v", err)
	}
}

func TestOnNewMember_NewMemberDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled: true,
		NewMember: config.NewMemberConfig{
			Enabled: false,
		},
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()
	roomID := TestRoomID(1)
	userID := TestUserID(1)

	err = manager.OnNewMember(ctx, roomID, userID)
	if err != nil {
		t.Errorf("OnNewMember() should return nil when new member welcome is disabled, got error: %v", err)
	}
}

func TestOnNewMember_RateLimited(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  1,
		MinIntervalMinutes: 60,
		NewMember: config.NewMemberConfig{
			Enabled:       true,
			WelcomePrompt: "欢迎新成员",
		},
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	stateTracker := NewStateTracker()
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, stateTracker, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()
	roomID := TestRoomID(1)
	userID := TestUserID(1)

	stateTracker.RecordProactiveMessage(roomID)

	err = manager.OnNewMember(ctx, roomID, userID)
	if err != nil {
		t.Errorf("OnNewMember() should return nil when rate limited, got error: %v", err)
	}

	state := stateTracker.GetState(roomID)
	if state.MessagesToday != 1 {
		t.Errorf("MessagesToday should be 1, got %d", state.MessagesToday)
	}
}

func TestCanSendMessage_DailyLimit(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  2,
		MinIntervalMinutes: 0,
		NewMember: config.NewMemberConfig{
			Enabled: true,
		},
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	stateTracker := NewStateTracker()
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, stateTracker, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	roomID := TestRoomID(1)

	if !manager.canSendMessage(roomID) {
		t.Error("canSendMessage() should return true for first message")
	}

	stateTracker.RecordProactiveMessage(roomID)

	if !manager.canSendMessage(roomID) {
		t.Error("canSendMessage() should return true for second message")
	}

	stateTracker.RecordProactiveMessage(roomID)

	if manager.canSendMessage(roomID) {
		t.Error("canSendMessage() should return false after reaching daily limit")
	}
}

func TestCanSendMessage_MinInterval(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 60,
		NewMember: config.NewMemberConfig{
			Enabled: true,
		},
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	stateTracker := NewStateTracker()
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, stateTracker, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	roomID := TestRoomID(1)

	if !manager.canSendMessage(roomID) {
		t.Error("canSendMessage() should return true for first message")
	}

	stateTracker.RecordProactiveMessage(roomID)

	if manager.canSendMessage(roomID) {
		t.Error("canSendMessage() should return false immediately after message due to min interval")
	}
}

func TestGenerateWelcomeMessage_AIDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled: true,
		NewMember: config.NewMemberConfig{
			Enabled:       true,
			WelcomePrompt: "欢迎新成员",
		},
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	// 验证管理器创建成功
	if manager == nil {
		t.Error("NewProactiveManager() returned nil manager")
	}
}

func TestTriggerCoordinator_NewCoordinator(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 60,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     60,
			CheckIntervalMinutes: 15,
		},
		Schedule: config.ScheduleConfig{
			Enabled: true,
			Times:   []string{"09:00", "18:00"},
		},
	}

	stateTracker := NewStateTracker()
	roomService := &FakeProactiveRooms{}

	silenceTrigger, err := NewSilenceTrigger(&cfg.Silence, stateTracker, roomService)
	if err != nil {
		t.Fatalf("NewSilenceTrigger() error = %v", err)
	}

	scheduleTrigger, err := NewScheduleTrigger(&cfg.Schedule)
	if err != nil {
		t.Fatalf("NewScheduleTrigger() error = %v", err)
	}

	rateLimiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}

	mockRL := &mockRoomListerTest{}
	coordinator, err := NewTriggerCoordinator(cfg, silenceTrigger, scheduleTrigger, rateLimiter, stateTracker, mockRL)
	if err != nil {
		t.Fatalf("NewTriggerCoordinator() error = %v", err)
	}

	if coordinator == nil {
		t.Fatal("NewTriggerCoordinator() returned nil")
	}

	// 验证 getter 方法
	if coordinator.GetSilenceTrigger() != silenceTrigger {
		t.Error("GetSilenceTrigger() did not return the expected trigger")
	}

	if coordinator.GetScheduleTrigger() != scheduleTrigger {
		t.Error("GetScheduleTrigger() did not return the expected trigger")
	}

	if coordinator.GetRateLimiter() != rateLimiter {
		t.Error("GetRateLimiter() did not return the expected limiter")
	}
}

func TestTriggerCoordinator_NilParameters(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{}
	stateTracker := NewStateTracker()
	roomService := &FakeProactiveRooms{}

	// 为测试创建有效的默认触发器
	defaultSilence, _ := NewSilenceTrigger(&cfg.Silence, stateTracker, roomService)
	defaultSchedule, _ := NewScheduleTrigger(&cfg.Schedule)
	defaultLimiter, _ := NewRateLimiter(cfg, stateTracker)

	tests := []struct {
		name         string
		cfg          *config.ProactiveConfig
		silence      *SilenceTrigger
		schedule     *ScheduleTrigger
		rateLimiter  *RateLimiter
		stateTracker *StateTracker
		wantErr      bool
		errContains  string
	}{
		{
			name:        "nil config",
			cfg:         nil,
			wantErr:     true,
			errContains: "主动聊天配置不能为空",
		},
		{
			name:         "nil silence trigger",
			cfg:          cfg,
			silence:      nil,
			schedule:     defaultSchedule,
			rateLimiter:  defaultLimiter,
			stateTracker: stateTracker,
			wantErr:      true,
			errContains:  "静默触发器不能为空",
		},
		{
			name:         "nil schedule trigger",
			cfg:          cfg,
			silence:      defaultSilence,
			schedule:     nil,
			rateLimiter:  defaultLimiter,
			stateTracker: stateTracker,
			wantErr:      true,
			errContains:  "定时触发器不能为空",
		},
		{
			name:         "nil rate limiter",
			cfg:          cfg,
			silence:      defaultSilence,
			schedule:     defaultSchedule,
			rateLimiter:  nil,
			stateTracker: stateTracker,
			wantErr:      true,
			errContains:  "速率限制器不能为空",
		},
		{
			name:         "nil state tracker",
			cfg:          cfg,
			silence:      defaultSilence,
			schedule:     defaultSchedule,
			rateLimiter:  defaultLimiter,
			stateTracker: nil,
			wantErr:      true,
			errContains:  "状态跟踪器不能为空",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockRL := &mockRoomListerTest{}
			coordinator, err := NewTriggerCoordinator(
				tt.cfg,
				tt.silence,
				tt.schedule,
				tt.rateLimiter,
				tt.stateTracker,
				mockRL,
			)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewTriggerCoordinator() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if tt.errContains != "" && err != nil {
					if !contains(err.Error(), tt.errContains) {
						t.Errorf("NewTriggerCoordinator() error = %v, want error containing %q", err, tt.errContains)
					}
				}
			} else {
				if coordinator == nil {
					t.Errorf("NewTriggerCoordinator() returned nil coordinator")
				}
			}
		})
	}
}

func TestTriggerCoordinator_CheckAndTrigger(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 5,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     60,
			CheckIntervalMinutes: 15,
		},
		Schedule: config.ScheduleConfig{
			Enabled: false,
		},
	}

	stateTracker := NewStateTracker()
	mockRL := &mockRoomListerTest{rooms: []chat.ConversationInfo{}}

	silenceTrigger, err := NewSilenceTrigger(&cfg.Silence, stateTracker, mockRL)
	if err != nil {
		t.Fatalf("NewSilenceTrigger() error = %v", err)
	}

	scheduleTrigger, err := NewScheduleTrigger(&cfg.Schedule)
	if err != nil {
		t.Fatalf("NewScheduleTrigger() error = %v", err)
	}

	rateLimiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}

	coordinator, err := NewTriggerCoordinator(cfg, silenceTrigger, scheduleTrigger, rateLimiter, stateTracker, mockRL)
	if err != nil {
		t.Fatalf("NewTriggerCoordinator() error = %v", err)
	}

	ctx := context.Background()

	// 测试无静默房间 - 应返回空切片（非 nil）
	results := coordinator.CheckAndTrigger(ctx)
	if results == nil && len(results) == 0 {
		// 空切片可接受，但 nil 不行
		t.Error("CheckAndTrigger() returned nil, want empty slice")
	}

	// 测试静默检测禁用的情况
	cfg.Silence.Enabled = false
	mockRL2 := &mockRoomListerTest{}
	coordinator2, err := NewTriggerCoordinator(cfg, silenceTrigger, scheduleTrigger, rateLimiter, stateTracker, mockRL2)
	if err != nil {
		t.Fatalf("NewTriggerCoordinator() error = %v", err)
	}

	results2 := coordinator2.CheckAndTrigger(ctx)
	if len(results2) != 0 {
		t.Errorf("CheckAndTrigger() returned %d results when silence disabled, want 0", len(results2))
	}
}

func TestTriggerCoordinator_HandleSilenceTrigger(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 5,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     60,
			CheckIntervalMinutes: 15,
		},
	}

	stateTracker := NewStateTracker()
	mockRL := &mockRoomListerTest{rooms: []chat.ConversationInfo{}}
	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, stateTracker, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	// 创建触发器并替换 manager 的 triggerCoord
	silenceTrigger, err := NewSilenceTrigger(&cfg.Silence, stateTracker, mockRL)
	if err != nil {
		t.Fatalf("NewSilenceTrigger() error = %v", err)
	}

	scheduleTrigger, err := NewScheduleTrigger(&cfg.Schedule)
	if err != nil {
		t.Fatalf("NewScheduleTrigger() error = %v", err)
	}

	rateLimiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}

	triggerCoord, err := NewTriggerCoordinator(cfg, silenceTrigger, scheduleTrigger, rateLimiter, stateTracker, mockRL)
	if err != nil {
		t.Fatalf("NewTriggerCoordinator() error = %v", err)
	}

	manager.triggerCoord = triggerCoord

	ctx := context.Background()

	// 这不应该 panic，应优雅处理空房间
	manager.handleSilenceTrigger(ctx)
}

func TestTriggerCoordinator_HandleScheduleTrigger(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 5,
		Schedule: config.ScheduleConfig{
			Enabled: true,
			Times:   []string{"09:00", "18:00"},
		},
	}

	stateTracker := NewStateTracker()
	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, stateTracker, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()

	// 这不应该 panic
	manager.handleScheduleTrigger(ctx)
}

// mockRoomListerTest 实现 ConversationLister 接口，用于 proactive_test.go 中的测试。
type mockRoomListerTest struct {
	rooms []chat.ConversationInfo
}

func (m *mockRoomListerTest) ListConversations(ctx context.Context) ([]chat.ConversationInfo, error) {
	return m.rooms, nil
}

// TestGenerateDefaultProactiveMessage_IsDirect 测试默认消息生成的私聊/群聊语气区分。
//
// 验证 generateDefaultProactiveMessage 方法根据 IsDirect 字段生成不同语气的消息：
// - 私聊：使用亲密、个人化的语气（如"你好"、"最近怎么样"）
// - 群聊：使用面向群体的语气（如"大家好"）
func TestGenerateDefaultProactiveMessage_IsDirect(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 60,
	}

	aiService := &Service{}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	tests := []struct {
		name             string
		triggerType      TriggerType
		isDirect         bool
		minutesSinceLast int
		wantContains     string
		wantNotContains  string
	}{
		{
			name:             "私聊 - 静默触发 - 长时间",
			triggerType:      TriggerInactivity,
			isDirect:         true,
			minutesSinceLast: 150,
			wantContains:     "好久没聊天了",
			wantNotContains:  "大家好",
		},
		{
			name:             "群聊 - 静默触发 - 长时间",
			triggerType:      TriggerInactivity,
			isDirect:         false,
			minutesSinceLast: 150,
			wantContains:     "大家好",
			wantNotContains:  "好久没聊天了",
		},
		{
			name:             "私聊 - 静默触发 - 短时间",
			triggerType:      TriggerInactivity,
			isDirect:         true,
			minutesSinceLast: 60,
			wantContains:     "在忙什么",
			wantNotContains:  "大家好",
		},
		{
			name:             "群聊 - 静默触发 - 短时间",
			triggerType:      TriggerInactivity,
			isDirect:         false,
			minutesSinceLast: 60,
			wantContains:     "大家好",
			wantNotContains:  "在忙什么",
		},
		{
			name:             "私聊 - 定时触发",
			triggerType:      TriggerScheduled,
			isDirect:         true,
			minutesSinceLast: 60,
			wantContains:     "希望你今天一切顺利",
			wantNotContains:  "大家好",
		},
		{
			name:             "群聊 - 定时触发",
			triggerType:      TriggerScheduled,
			isDirect:         false,
			minutesSinceLast: 60,
			wantContains:     "大家好",
			wantNotContains:  "希望你今天一切顺利",
		},
		{
			name:             "私聊 - 默认触发",
			triggerType:      TriggerType("unknown"),
			isDirect:         true,
			minutesSinceLast: 60,
			wantContains:     "你好",
			wantNotContains:  "大家好",
		},
		{
			name:             "群聊 - 默认触发",
			triggerType:      TriggerType("unknown"),
			isDirect:         false,
			minutesSinceLast: 60,
			wantContains:     "大家好",
			wantNotContains:  "你好",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decisionCtx := &DecisionContext{
				IsDirect:         tt.isDirect,
				MinutesSinceLast: tt.minutesSinceLast,
			}

			msg := manager.generateDefaultProactiveMessage(tt.triggerType, decisionCtx)

			if !contains(msg, tt.wantContains) {
				t.Errorf("generateDefaultProactiveMessage() = %q, want to contain %q", msg, tt.wantContains)
			}

			if tt.wantNotContains != "" && contains(msg, tt.wantNotContains) {
				t.Errorf("generateDefaultProactiveMessage() = %q, should not contain %q", msg, tt.wantNotContains)
			}
		})
	}
}

// TestGenerateWelcomeMessage_IsDirect 测试欢迎消息生成的私聊/群聊语气区分。
//
// 验证 generateWelcomeMessage 方法根据 IsDirect 参数生成不同语气的欢迎消息：
// - 私聊：使用"很高兴认识你"等亲密语气
// - 群聊：使用"欢迎加入"等群体语气
func TestGenerateWelcomeMessage_IsDirect(t *testing.T) {
	t.Parallel()

	cfg := &config.ProactiveConfig{
		Enabled: true,
		NewMember: config.NewMemberConfig{
			Enabled:       true,
			WelcomePrompt: "欢迎新成员",
		},
	}

	// 创建一个带有 core 的 Service，Enabled=false 表示 AI 未启用
	core, coreErr := NewCore(&config.AIConfig{Enabled: false})
	if coreErr != nil {
		t.Fatal(coreErr)
	}
	aiService := &Service{core: core}
	roomService := &FakeProactiveRooms{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, nil, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()
	userID := TestUserID(1)

	tests := []struct {
		name            string
		isDirect        bool
		wantContains    string
		wantNotContains string
	}{
		{
			name:            "私聊欢迎",
			isDirect:        true,
			wantContains:    "很高兴认识你",
			wantNotContains: "欢迎",
		},
		{
			name:            "群聊欢迎",
			isDirect:        false,
			wantContains:    "欢迎",
			wantNotContains: "很高兴认识你",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := manager.generateWelcomeMessage(ctx, userID, tt.isDirect)
			if err != nil {
				t.Fatalf("generateWelcomeMessage() error = %v", err)
			}

			if !contains(msg, tt.wantContains) {
				t.Errorf("generateWelcomeMessage() = %q, want to contain %q", msg, tt.wantContains)
			}

			if tt.wantNotContains != "" && contains(msg, tt.wantNotContains) {
				t.Errorf("generateWelcomeMessage() = %q, should not contain %q", msg, tt.wantNotContains)
			}
		})
	}
}

// TestRateLimiter_GetDailyLimit 测试 GetDailyLimit 方法。
func TestRateLimiter_GetDailyLimit(t *testing.T) {
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 30,
	}
	stateTracker := NewStateTracker()
	limiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}

	if limit := limiter.GetDailyLimit(); limit != 10 {
		t.Errorf("GetDailyLimit() = %d, want 10", limit)
	}
}

// TestRateLimiter_GetMinInterval 测试 GetMinInterval 方法。
func TestRateLimiter_GetMinInterval(t *testing.T) {
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 45,
	}
	stateTracker := NewStateTracker()
	limiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}

	interval := limiter.GetMinInterval()
	want := 45 * time.Minute
	if interval != want {
		t.Errorf("GetMinInterval() = %v, want %v", interval, want)
	}
}

// TestRateLimiter_GetRemainingMessages 测试 GetRemainingMessages 方法。
func TestRateLimiter_GetRemainingMessages(t *testing.T) {
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 30,
	}
	stateTracker := NewStateTracker()
	limiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}

	roomID := id.RoomID("!room:example.com")

	// 无消息时，返回最大值
	if remaining := limiter.GetRemainingMessages(roomID); remaining != 5 {
		t.Errorf("GetRemainingMessages() = %d, want 5", remaining)
	}

	// 记录一些消息
	stateTracker.RecordProactiveMessage(roomID)
	stateTracker.RecordProactiveMessage(roomID)

	if remaining := limiter.GetRemainingMessages(roomID); remaining != 3 {
		t.Errorf("GetRemainingMessages() = %d, want 3", remaining)
	}

	// 超过限制
	for i := 0; i < 5; i++ {
		stateTracker.RecordProactiveMessage(roomID)
	}

	if remaining := limiter.GetRemainingMessages(roomID); remaining != 0 {
		t.Errorf("GetRemainingMessages() = %d, want 0", remaining)
	}
}

// TestRateLimiter_TimeUntilNextAllowed 测试 TimeUntilNextAllowed 方法。
func TestRateLimiter_TimeUntilNextAllowed(t *testing.T) {
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 30,
	}
	stateTracker := NewStateTracker()
	limiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}

	roomID := id.RoomID("!room:example.com")

	// 无消息时，可以立即发送
	if wait := limiter.TimeUntilNextAllowed(roomID); wait != 0 {
		t.Errorf("TimeUntilNextAllowed() = %v, want 0", wait)
	}

	// 记录消息
	stateTracker.RecordProactiveMessage(roomID)

	// 应该需要等待
	wait := limiter.TimeUntilNextAllowed(roomID)
	if wait <= 0 || wait > 30*time.Minute {
		t.Errorf("TimeUntilNextAllowed() = %v, want between 0 and 30m", wait)
	}
}

// TestSilenceTrigger_Methods 测试 SilenceTrigger 的辅助方法。
func TestSilenceTrigger_Methods(t *testing.T) {
	cfg := &config.SilenceConfig{
		Enabled:              true,
		ThresholdMinutes:     60,
		CheckIntervalMinutes: 15,
	}
	stateTracker := NewStateTracker()

	// 创建 mock ConversationLister
	mockRL := &mockRoomListerTest{}

	trigger, err := NewSilenceTrigger(cfg, stateTracker, mockRL)
	if err != nil {
		t.Fatalf("NewSilenceTrigger() error = %v", err)
	}

	// 测试 IsEnabled
	if !trigger.IsEnabled() {
		t.Error("IsEnabled() = false, want true")
	}

	// 测试 GetThreshold
	threshold := trigger.GetThreshold()
	wantThreshold := 60 * time.Minute
	if threshold != wantThreshold {
		t.Errorf("GetThreshold() = %v, want %v", threshold, wantThreshold)
	}

	// 测试 GetCheckInterval
	interval := trigger.GetCheckInterval()
	wantInterval := 15 * time.Minute
	if interval != wantInterval {
		t.Errorf("GetCheckInterval() = %v, want %v", interval, wantInterval)
	}
}

// TestScheduleTrigger_Methods 测试 ScheduleTrigger 的辅助方法。
func TestScheduleTrigger_Methods(t *testing.T) {
	cfg := &config.ScheduleConfig{
		Enabled: true,
		Times:   []string{"09:00", "18:30"},
	}

	trigger, err := NewScheduleTrigger(cfg)
	if err != nil {
		t.Fatalf("NewScheduleTrigger() error = %v", err)
	}

	// 测试 GetScheduledTimes
	times := trigger.GetScheduledTimes()
	if len(times) != 2 {
		t.Errorf("GetScheduledTimes() returned %d times, want 2", len(times))
	}

	// 测试 IsTriggeredToday（初始状态）
	if trigger.IsTriggeredToday("09:00") {
		t.Error("IsTriggeredToday() = true initially, want false")
	}

	// 测试 Reset
	trigger.Reset()
	if trigger.IsTriggeredToday("09:00") {
		t.Error("IsTriggeredToday() = true after Reset, want false")
	}
}

// TestScheduleTrigger_Check 测试 ScheduleTrigger 的 Check 方法。
func TestScheduleTrigger_Check(t *testing.T) {
	cfg := &config.ScheduleConfig{
		Enabled: true,
		Times:   []string{"09:00"},
	}

	trigger, err := NewScheduleTrigger(cfg)
	if err != nil {
		t.Fatalf("NewScheduleTrigger() error = %v", err)
	}

	// Check 方法返回 false 因为当前时间不匹配
	triggered := trigger.Check(context.Background())
	// 我们无法预测确切的时间匹配，但可以验证方法不会 panic
	_ = triggered
}

// TestSilenceTrigger_Check 测试 SilenceTrigger 的 Check 方法。
func TestSilenceTrigger_Check(t *testing.T) {
	cfg := &config.SilenceConfig{
		Enabled:              true,
		ThresholdMinutes:     60,
		CheckIntervalMinutes: 15,
	}
	stateTracker := NewStateTracker()
	mockRL := &mockRoomListerTest{}

	trigger, err := NewSilenceTrigger(cfg, stateTracker, mockRL)
	if err != nil {
		t.Fatalf("NewSilenceTrigger() error = %v", err)
	}

	// Check 方法测试
	silentRooms, err := trigger.Check(context.Background())
	// 验证方法不会 panic
	_ = silentRooms
	_ = err
}

// TestProactiveManager_SendMessage_RoutesThroughPort 断言主动投递按 isNotice 选择端口方法、
// 带上正确的会话标识，并且只有发送成功才计入频控。
func TestProactiveManager_SendMessage_RoutesThroughPort(t *testing.T) {
	ctx := context.Background()
	roomID := TestRoomID(7)

	for _, tt := range []struct {
		name   string
		notice bool
	}{
		{name: "普通消息走 SendText", notice: false},
		{name: "通知消息走 SendNotice", notice: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rooms := &FakeProactiveRooms{}
			manager := newProactiveManagerForSendTest(t, rooms)

			if err := manager.SendMessage(ctx, roomID, "主动内容", tt.notice); err != nil {
				t.Fatalf("SendMessage() error = %v", err)
			}

			if len(rooms.Sent) != 1 {
				t.Fatalf("Sent 长度 = %d, want 1", len(rooms.Sent))
			}
			sent := rooms.Sent[0]
			if sent.Conversation != roomID.String() {
				t.Errorf("Conversation = %q, want %q", sent.Conversation, roomID.String())
			}
			if sent.Text != "主动内容" {
				t.Errorf("Text = %q, want 主动内容", sent.Text)
			}
			if sent.Notice != tt.notice {
				t.Errorf("Notice = %v, want %v", sent.Notice, tt.notice)
			}
			if got := manager.stateTracker.GetState(roomID).MessagesToday; got != 1 {
				t.Errorf("MessagesToday = %d, want 1", got)
			}
		})
	}

	t.Run("空正文不投递", func(t *testing.T) {
		rooms := &FakeProactiveRooms{}
		manager := newProactiveManagerForSendTest(t, rooms)

		if err := manager.SendMessage(ctx, roomID, "", false); err == nil {
			t.Error("SendMessage() 空正文应返回错误")
		}
		if len(rooms.Sent) != 0 {
			t.Errorf("Sent = %+v, want 空", rooms.Sent)
		}
	})

	t.Run("投递失败不计频控", func(t *testing.T) {
		rooms := &FakeProactiveRooms{SendErr: errors.New("homeserver 不可达")}
		manager := newProactiveManagerForSendTest(t, rooms)

		if err := manager.SendMessage(ctx, roomID, "主动内容", false); err == nil {
			t.Error("SendMessage() 端口报错时应返回错误")
		}
		if len(rooms.Sent) != 0 {
			t.Errorf("Sent = %+v, want 空", rooms.Sent)
		}
		if got := manager.stateTracker.GetState(roomID).MessagesToday; got != 0 {
			t.Errorf("MessagesToday = %d, want 0", got)
		}
	})
}

// newProactiveManagerForSendTest 构造一个只依赖房间端口替身的主动聊天管理器。
func newProactiveManagerForSendTest(t *testing.T, rooms ProactiveRooms) *ProactiveManager {
	t.Helper()

	core, err := NewCore(&config.AIConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}

	manager, err := NewProactiveManager(
		&config.ProactiveConfig{
			Enabled:            true,
			MaxMessagesPerDay:  5,
			MinIntervalMinutes: 1,
			NewMember:          config.NewMemberConfig{Enabled: true, WelcomePrompt: "欢迎新成员"},
		},
		&Service{core: core},
		rooms,
		nil,
		&config.AIConfig{},
	)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	return manager
}

// TestProactiveManager_OnNewMember_WelcomesThroughPort 断言新成员欢迎按会话元数据选择语气、
// 投递到正确会话，并在元数据缺失时降级为群聊而不是静默失败。
func TestProactiveManager_OnNewMember_WelcomesThroughPort(t *testing.T) {
	ctx := context.Background()
	roomID := TestRoomID(8)
	userID := TestUserID(8)

	tests := []struct {
		name            string
		info            chat.ConversationInfo
		infoErr         error
		wantContains    string
		wantNotContains string
	}{
		{
			name:            "两人会话按私聊语气",
			info:            chat.ConversationInfo{Conversation: roomID.String(), Name: "私聊", MemberCount: 2},
			wantContains:    "很高兴认识你",
			wantNotContains: "加入",
		},
		{
			name:            "多人会话按群聊语气",
			info:            chat.ConversationInfo{Conversation: roomID.String(), Name: "群聊", MemberCount: 5},
			wantContains:    "加入",
			wantNotContains: "很高兴认识你",
		},
		{
			name:            "元数据不可得降级为群聊",
			infoErr:         errors.New("房间服务不可用"),
			wantContains:    "加入",
			wantNotContains: "很高兴认识你",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rooms := &FakeProactiveRooms{Info: tt.info, InfoErr: tt.infoErr}
			manager := newProactiveManagerForSendTest(t, rooms)

			if err := manager.OnNewMember(ctx, roomID, userID); err != nil {
				t.Fatalf("OnNewMember() error = %v", err)
			}

			sent, ok := rooms.SentTo(roomID.String())
			if !ok {
				t.Fatalf("Sent = %+v, want 投递到 %s", rooms.Sent, roomID)
			}
			if !contains(sent.Text, tt.wantContains) {
				t.Errorf("欢迎消息 = %q, want 含 %q", sent.Text, tt.wantContains)
			}
			if tt.wantNotContains != "" && contains(sent.Text, tt.wantNotContains) {
				t.Errorf("欢迎消息 = %q, 不应含 %q", sent.Text, tt.wantNotContains)
			}
			if sent.Notice {
				t.Error("欢迎消息应以普通消息投递")
			}
			if got := manager.stateTracker.GetState(roomID).MessagesToday; got != 1 {
				t.Errorf("MessagesToday = %d, want 1", got)
			}
		})
	}
}

// TestSilenceTrigger_Check_MapsConversationsToRoomIDs 断言静默检测把平台会话标识映射回
// 状态跟踪器使用的房间 ID，并且只报告超过阈值的会话。
func TestSilenceTrigger_Check_MapsConversationsToRoomIDs(t *testing.T) {
	silentRoom := id.RoomID("!silent:example.com")
	activeRoom := id.RoomID("!active:example.com")

	rooms := &FakeProactiveRooms{Conversations: []chat.ConversationInfo{
		{Conversation: silentRoom.String(), Name: "沉默房间"},
		{Conversation: activeRoom.String(), Name: "活跃房间"},
	}}
	stateTracker := NewStateTracker()
	stateTracker.RecordUserMessage(activeRoom)

	trigger, err := NewSilenceTrigger(&config.SilenceConfig{
		Enabled:              true,
		ThresholdMinutes:     5,
		CheckIntervalMinutes: 1,
	}, stateTracker, rooms)
	if err != nil {
		t.Fatalf("NewSilenceTrigger() error = %v", err)
	}

	silent, err := trigger.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if len(silent) != 1 {
		t.Fatalf("Check() = %+v, want 仅 1 个静默会话", silent)
	}
	if silent[0].RoomID != silentRoom {
		t.Errorf("RoomID = %q, want %q", silent[0].RoomID, silentRoom)
	}
	if silent[0].SilentDuration <= 0 {
		t.Errorf("SilentDuration = %v, want 正值", silent[0].SilentDuration)
	}
}

// TestSilenceTrigger_Check_ListError 断言会话枚举失败时错误向上冒泡并保留上下文。
func TestSilenceTrigger_Check_ListError(t *testing.T) {
	rooms := &FakeProactiveRooms{ListErr: errors.New("网络中断")}

	trigger, err := NewSilenceTrigger(&config.SilenceConfig{
		Enabled:              true,
		ThresholdMinutes:     5,
		CheckIntervalMinutes: 1,
	}, NewStateTracker(), rooms)
	if err != nil {
		t.Fatalf("NewSilenceTrigger() error = %v", err)
	}

	if _, err := trigger.Check(context.Background()); err == nil {
		t.Fatal("Check() 应返回错误")
	} else if !contains(err.Error(), "获取会话列表失败") {
		t.Errorf("Check() error = %v, want 含「获取会话列表失败」", err)
	}
}

// TestTriggerCoordinator_CheckScheduleTrigger_MapsConversations 断言定时触发按会话枚举产出结果，
// 并对超频会话给出被速率限制阻止的结论。
func TestTriggerCoordinator_CheckScheduleTrigger_MapsConversations(t *testing.T) {
	limitedRoom := id.RoomID("!limited:example.com")
	openRoom := id.RoomID("!open:example.com")

	rooms := &FakeProactiveRooms{Conversations: []chat.ConversationInfo{
		{Conversation: limitedRoom.String(), Name: "已超频"},
		{Conversation: openRoom.String(), Name: "可说话"},
	}}
	cfg := &config.ProactiveConfig{
		Enabled:           true,
		MaxMessagesPerDay: 1,
		Silence: config.SilenceConfig{
			Enabled:              false,
			ThresholdMinutes:     5,
			CheckIntervalMinutes: 1,
		},
	}
	stateTracker := NewStateTracker()
	stateTracker.RecordProactiveMessage(limitedRoom)

	silenceTrigger, err := NewSilenceTrigger(&cfg.Silence, stateTracker, rooms)
	if err != nil {
		t.Fatalf("NewSilenceTrigger() error = %v", err)
	}
	rateLimiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}

	// 定时触发只在当前分钟命中，跨分钟重建一次以免误判。
	var results []TriggerResult
	for attempt := 0; attempt < 3 && len(results) == 0; attempt++ {
		scheduleTrigger, err := NewScheduleTrigger(&config.ScheduleConfig{
			Enabled: true,
			Times:   []string{time.Now().Format("15:04")},
		})
		if err != nil {
			t.Fatalf("NewScheduleTrigger() error = %v", err)
		}
		coordinator, err := NewTriggerCoordinator(cfg, silenceTrigger, scheduleTrigger, rateLimiter, stateTracker, rooms)
		if err != nil {
			t.Fatalf("NewTriggerCoordinator() error = %v", err)
		}
		results = coordinator.checkScheduleTrigger(context.Background())
	}

	if len(results) != 2 {
		t.Fatalf("checkScheduleTrigger() = %+v, want 2 条结果", results)
	}

	byRoom := make(map[id.RoomID]TriggerResult, len(results))
	for _, r := range results {
		byRoom[r.RoomID] = r
	}
	if got, ok := byRoom[openRoom]; !ok {
		t.Errorf("结果缺少会话 %s，得到 %+v", openRoom, results)
	} else if !got.ShouldTrigger {
		t.Errorf("%s ShouldTrigger = false, want true（Reason=%q）", openRoom, got.Reason)
	}
	if got, ok := byRoom[limitedRoom]; !ok {
		t.Errorf("结果缺少会话 %s，得到 %+v", limitedRoom, results)
	} else if got.ShouldTrigger {
		t.Errorf("%s ShouldTrigger = true, want false（已达每日上限）", limitedRoom)
	} else if !contains(got.Reason, "速率限制") {
		t.Errorf("%s Reason = %q, want 说明被速率限制阻止", limitedRoom, got.Reason)
	}
}

// TestProactiveManager_HandleProactiveTrigger_UsesCachedDecision 断言缓存决策命中时不会再调用
// 决策模型，并直接把缓存正文投递到触发的会话。
func TestProactiveManager_HandleProactiveTrigger_UsesCachedDecision(t *testing.T) {
	ctx := context.Background()
	roomID := TestRoomID(9)

	tests := []struct {
		name        string
		shouldSpeak bool
		wantSent    bool
	}{
		{name: "命中且决定发言", shouldSpeak: true, wantSent: true},
		{name: "命中但决定沉默", shouldSpeak: false, wantSent: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rooms := &FakeProactiveRooms{Info: chat.ConversationInfo{Name: "群聊", MemberCount: 5}}
			manager := newProactiveManagerForSendTest(t, rooms)
			threshold := manager.config.Silence.ThresholdMinutes

			decisionCtx, err := GatherDecisionContext(ctx, roomID, manager.stateTracker, rooms, TriggerScheduled, threshold)
			if err != nil {
				t.Fatalf("GatherDecisionContext() error = %v", err)
			}
			manager.decisionCache.Set(roomID, decisionCtx, &DecisionResponse{
				ShouldSpeak: tt.shouldSpeak,
				Content:     "缓存的主动消息",
				Reason:      "测试缓存",
			})

			if err := manager.handleProactiveTrigger(ctx, roomID, TriggerScheduled); err != nil {
				t.Fatalf("handleProactiveTrigger() error = %v", err)
			}

			sent, ok := rooms.SentTo(roomID.String())
			if ok != tt.wantSent {
				t.Fatalf("Sent = %+v, want 投递=%v", rooms.Sent, tt.wantSent)
			}
			if tt.wantSent && sent.Text != "缓存的主动消息" {
				t.Errorf("Text = %q, want 缓存的主动消息", sent.Text)
			}
			if tt.wantSent && sent.Notice {
				t.Error("缓存决策正文应以普通消息投递")
			}
		})
	}
}
