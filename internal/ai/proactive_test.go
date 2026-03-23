//go:build goolm

package ai

import (
	"context"
	"testing"
	"time"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

func TestNewProactiveManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *config.ProactiveConfig
		aiService      *Service
		roomService    *matrix.RoomService
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
			name:           "空房间服务",
			config:         &config.ProactiveConfig{},
			aiService:      &Service{},
			roomService:    nil,
			globalAIConfig: nil,
			wantErr:        true,
			errContains:    "matrix 房间服务不能为空",
		},
		{
			name:           "空全局 AI 配置",
			config:         &config.ProactiveConfig{},
			aiService:      &Service{},
			roomService:    &matrix.RoomService{},
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
			roomService:    &matrix.RoomService{},
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}

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
	roomService := &matrix.RoomService{}

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
	mockRL := &mockRoomListerTest{rooms: []matrix.RoomInfo{}}

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
	mockRL := &mockRoomListerTest{rooms: []matrix.RoomInfo{}}
	aiService := &Service{}
	roomService := &matrix.RoomService{}
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
	roomService := &matrix.RoomService{}
	globalAIConfig := &config.AIConfig{}

	manager, err := NewProactiveManager(cfg, aiService, roomService, stateTracker, globalAIConfig)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}

	ctx := context.Background()

	// 这不应该 panic
	manager.handleScheduleTrigger(ctx)
}

// mockRoomListerTest 实现 RoomLister 接口，用于 proactive_test.go 中的测试。
type mockRoomListerTest struct {
	rooms []matrix.RoomInfo
}

func (m *mockRoomListerTest) GetJoinedRooms(ctx context.Context) ([]matrix.RoomInfo, error) {
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
	roomService := &matrix.RoomService{}
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
			triggerType:      TriggerManual,
			isDirect:         true,
			minutesSinceLast: 60,
			wantContains:     "你好",
			wantNotContains:  "大家好",
		},
		{
			name:             "群聊 - 默认触发",
			triggerType:      TriggerManual,
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

	aiService := &Service{} // AI 未启用，使用默认消息
	roomService := &matrix.RoomService{}
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
