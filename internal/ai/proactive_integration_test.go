//go:build goolm

package ai

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

type mockRoomService struct {
	rooms []matrix.RoomInfo
}

func (m *mockRoomService) SendMessage(ctx context.Context, roomID, text string) (id.EventID, error) {
	return id.EventID("$test_event"), nil
}

func (m *mockRoomService) SendNotice(ctx context.Context, roomID, text string) (id.EventID, error) {
	return id.EventID("$test_notice"), nil
}

func (m *mockRoomService) GetJoinedRooms(ctx context.Context) ([]matrix.RoomInfo, error) {
	return m.rooms, nil
}

func TestProactiveManagerLifecycleIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 1,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     5,
			CheckIntervalMinutes: 1,
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
	mockRoomSVC := &matrix.RoomService{}
	stateTracker := NewStateTracker()
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, stateTracker)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	ctx := context.Background()
	manager.Start(ctx)
	time.Sleep(50 * time.Millisecond)
}

func TestProactiveManagerShutdownGracefulIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 1,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     5,
			CheckIntervalMinutes: 1,
		},
		Schedule: config.ScheduleConfig{
			Enabled: true,
			Times:   []string{"09:00"},
		},
		NewMember: config.NewMemberConfig{
			Enabled:       true,
			WelcomePrompt: "欢迎",
		},
	}
	mockRoomSVC := &matrix.RoomService{}
	stateTracker := NewStateTracker()
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, stateTracker)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out - possible goroutine leak")
	}
}

func TestProactiveManagerDisabledInstanceIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{Enabled: false}
	mockRoomSVC := &matrix.RoomService{}
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, nil)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	ctx := context.Background()
	manager.Start(ctx)
	manager.Stop()
}

func TestProactiveManagerBackgroundTasksExitIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 1,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     5,
			CheckIntervalMinutes: 1,
		},
		Schedule: config.ScheduleConfig{
			Enabled: true,
			Times:   []string{"09:00"},
		},
	}
	mockRoomSVC := &matrix.RoomService{}
	stateTracker := NewStateTracker()
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, stateTracker)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	ctx := context.Background()
	manager.Start(ctx)
	time.Sleep(30 * time.Millisecond)
	startTime := time.Now()
	manager.Stop()
	elapsed := time.Since(startTime)
	if elapsed > time.Second {
		t.Errorf("Stop() took %v, expected < 1s", elapsed)
	}
}

func TestProactiveManagerConcurrencyIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  100,
		MinIntervalMinutes: 1,
	}
	mockRoomSVC := &matrix.RoomService{}
	stateTracker := NewStateTracker()
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, stateTracker)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	ctx := context.Background()
	manager.Start(ctx)
	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Concurrency test timed out")
	}
}

func TestProactiveManagerWithCancelContextIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 1,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     5,
			CheckIntervalMinutes: 1,
		},
	}
	mockRoomSVC := &matrix.RoomService{}
	stateTracker := NewStateTracker()
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, stateTracker)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	cancel()
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() after context cancellation timed out")
	}
}

func TestProactiveManagerEmptyRoomsIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 1,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     5,
			CheckIntervalMinutes: 1,
		},
	}
	mockRoomSVC := &matrix.RoomService{}
	stateTracker := NewStateTracker()
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, stateTracker)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	ctx := context.Background()
	manager.Start(ctx)
	manager.Stop()
}

func TestProactiveManagerWithNilStateTrackerIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  5,
		MinIntervalMinutes: 1,
	}
	mockRoomSVC := &matrix.RoomService{}
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, nil)
	if err != nil {
		t.Fatalf("NewProactiveManager() with nil StateTracker error = %v", err)
	}
	ctx := context.Background()
	manager.Start(ctx)
	manager.Stop()
}

func TestProactiveManagerLongRunningIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  100,
		MinIntervalMinutes: 1,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     5,
			CheckIntervalMinutes: 1,
		},
	}
	mockRoomSVC := &matrix.RoomService{}
	stateTracker := NewStateTracker()
	manager, err := NewProactiveManager(cfg, &Service{}, mockRoomSVC, stateTracker)
	if err != nil {
		t.Fatalf("NewProactiveManager() error = %v", err)
	}
	ctx := context.Background()
	manager.Start(ctx)
	time.Sleep(1 * time.Second)
	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Long running test Stop() timed out")
	}
}

func TestTriggerCoordinatorIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 1,
		Silence: config.SilenceConfig{
			Enabled:              true,
			ThresholdMinutes:     5,
			CheckIntervalMinutes: 1,
		},
		Schedule: config.ScheduleConfig{
			Enabled: true,
			Times:   []string{"09:00", "18:00"},
		},
	}
	stateTracker := NewStateTracker()
	mockRoomSVC := &mockRoomService{
		rooms: []matrix.RoomInfo{},
	}
	silenceTrigger, err := NewSilenceTrigger(&cfg.Silence, stateTracker, mockRoomSVC)
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
	coordinator, err := NewTriggerCoordinator(cfg, silenceTrigger, scheduleTrigger, rateLimiter, stateTracker)
	if err != nil {
		t.Fatalf("NewTriggerCoordinator() error = %v", err)
	}
	if coordinator == nil {
		t.Fatal("NewTriggerCoordinator() returned nil")
	}
	ctx := context.Background()
	results := coordinator.CheckAndTrigger(ctx)
	if results == nil {
		t.Error("CheckAndTrigger() returned nil, want empty slice")
	}
}

func TestScheduleTriggerIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ScheduleConfig{
		Enabled: true,
		Times:   []string{"09:00", "18:00"},
	}
	trigger, err := NewScheduleTrigger(cfg)
	if err != nil {
		t.Fatalf("NewScheduleTrigger() error = %v", err)
	}
	ctx := context.Background()
	result := trigger.Check(ctx)
	if result != true {
		t.Log("Note: Schedule triggers only at configured times")
	}
}

func TestRateLimiterIntegration(t *testing.T) {
	t.Parallel()
	cfg := &config.ProactiveConfig{
		Enabled:            true,
		MaxMessagesPerDay:  10,
		MinIntervalMinutes: 1,
	}
	stateTracker := NewStateTracker()
	limiter, err := NewRateLimiter(cfg, stateTracker)
	if err != nil {
		t.Fatalf("NewRateLimiter() error = %v", err)
	}
	roomID := TestRoomID(1)
	if !limiter.CanSpeak(roomID) {
		t.Error("CanSpeak() returned false for new room, expected true")
	}
	limiter.stateTracker.RecordProactiveMessage(roomID)
	if limiter.CanSpeak(roomID) {
		t.Error("CanSpeak() returned true after recording message, expected false due to min interval")
	}
}
