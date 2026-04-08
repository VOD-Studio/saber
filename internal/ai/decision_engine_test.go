//go:build goolm

// Package ai 提供 AI 决策引擎测试。
package ai

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// mockAIServiceForDecide 是 AIService 接口的模拟实现，专门用于测试 Decide 函数。
type mockAIServiceForDecide struct {
	enabled           bool
	simpleResponse    string
	simpleErr         error
	streamingResponse string
	streamingErr      error
}

func (m *mockAIServiceForDecide) IsEnabled() bool {
	return m.enabled
}

func (m *mockAIServiceForDecide) GenerateSimpleResponse(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	return m.simpleResponse, m.simpleErr
}

func (m *mockAIServiceForDecide) GenerateSimpleResponseWithModel(ctx context.Context, modelName string, temperature float64, systemPrompt, userMessage string) (string, error) {
	return m.simpleResponse, m.simpleErr
}

func (m *mockAIServiceForDecide) GenerateStreamingSimpleResponse(ctx context.Context, modelName string, temperature float64, systemPrompt, userMessage string) (string, error) {
	return m.streamingResponse, m.streamingErr
}

// TestDecisionEngine_Decide_AIEnabled 测试 Decide 函数当 AI 启用时的行为。
func TestDecisionEngine_Decide_AIEnabled(t *testing.T) {
	t.Run("AI disabled returns false", func(t *testing.T) {
		mockSvc := &mockAIServiceForDecide{enabled: false}
		engine, err := NewDecisionEngine(mockSvc, &config.DecisionConfig{}, &config.AIConfig{})
		if err != nil {
			t.Fatalf("NewDecisionEngine failed: %v", err)
		}

		decisionCtx := &DecisionContext{
			RoomID:           id.RoomID("!test:example.org"),
			ActivityLevel:    ActivityLow,
			MinutesSinceLast: 120,
			MessagesToday:    0,
		}

		shouldSpeak, content, err := engine.Decide(context.Background(), decisionCtx)
		if err != nil {
			t.Errorf("Decide should not return error when AI disabled: %v", err)
		}
		if shouldSpeak {
			t.Error("shouldSpeak should be false when AI disabled")
		}
		if content != "" {
			t.Error("content should be empty when AI disabled")
		}
	})

	t.Run("non-streaming success", func(t *testing.T) {
		jsonResponse := `{"should_speak": true, "reason": "test reason", "content": "Hello!"}`
		mockSvc := &mockAIServiceForDecide{
			enabled:        true,
			simpleResponse: jsonResponse,
		}
		engine, err := NewDecisionEngine(mockSvc, &config.DecisionConfig{StreamEnabled: false}, &config.AIConfig{})
		if err != nil {
			t.Fatalf("NewDecisionEngine failed: %v", err)
		}

		decisionCtx := &DecisionContext{
			RoomID:           id.RoomID("!test:example.org"),
			ActivityLevel:    ActivityLow,
			MinutesSinceLast: 120,
			MessagesToday:    0,
		}

		shouldSpeak, content, err := engine.Decide(context.Background(), decisionCtx)
		if err != nil {
			t.Errorf("Decide returned error: %v", err)
		}
		if !shouldSpeak {
			t.Error("shouldSpeak should be true")
		}
		if content != "Hello!" {
			t.Errorf("content = %q, want %q", content, "Hello!")
		}
	})

	t.Run("streaming success", func(t *testing.T) {
		jsonResponse := `{"should_speak": false, "reason": "test reason", "content": ""}`
		mockSvc := &mockAIServiceForDecide{
			enabled:           true,
			streamingResponse: jsonResponse,
		}
		engine, err := NewDecisionEngine(mockSvc, &config.DecisionConfig{StreamEnabled: true}, &config.AIConfig{})
		if err != nil {
			t.Fatalf("NewDecisionEngine failed: %v", err)
		}

		decisionCtx := &DecisionContext{
			RoomID:           id.RoomID("!test:example.org"),
			ActivityLevel:    ActivityHigh,
			MinutesSinceLast: 10,
			MessagesToday:    0,
		}

		shouldSpeak, content, err := engine.Decide(context.Background(), decisionCtx)
		if err != nil {
			t.Errorf("Decide returned error: %v", err)
		}
		if shouldSpeak {
			t.Error("shouldSpeak should be false")
		}
		if content != "" {
			t.Errorf("content = %q, want empty", content)
		}
	})

	t.Run("AI error", func(t *testing.T) {
		mockSvc := &mockAIServiceForDecide{
			enabled:   true,
			simpleErr: errors.New("AI service error"),
		}
		engine, err := NewDecisionEngine(mockSvc, &config.DecisionConfig{StreamEnabled: false}, &config.AIConfig{})
		if err != nil {
			t.Fatalf("NewDecisionEngine failed: %v", err)
		}

		decisionCtx := &DecisionContext{
			RoomID:           id.RoomID("!test:example.org"),
			ActivityLevel:    ActivityLow,
			MinutesSinceLast: 120,
			MessagesToday:    0,
		}

		_, _, err = engine.Decide(context.Background(), decisionCtx)
		if err == nil {
			t.Error("Decide should return error when AI fails")
		}
	})

	t.Run("streaming AI error", func(t *testing.T) {
		mockSvc := &mockAIServiceForDecide{
			enabled:      true,
			streamingErr: errors.New("streaming AI error"),
		}
		engine, err := NewDecisionEngine(mockSvc, &config.DecisionConfig{StreamEnabled: true}, &config.AIConfig{})
		if err != nil {
			t.Fatalf("NewDecisionEngine failed: %v", err)
		}

		decisionCtx := &DecisionContext{
			RoomID:           id.RoomID("!test:example.org"),
			ActivityLevel:    ActivityLow,
			MinutesSinceLast: 120,
			MessagesToday:    0,
		}

		_, _, err = engine.Decide(context.Background(), decisionCtx)
		if err == nil {
			t.Error("Decide should return error when streaming AI fails")
		}
	})

	t.Run("with custom model", func(t *testing.T) {
		jsonResponse := `{"should_speak": true, "reason": "test", "content": "Hi!"}`
		mockSvc := &mockAIServiceForDecide{
			enabled:        true,
			simpleResponse: jsonResponse,
		}
		cfg := &config.DecisionConfig{
			StreamEnabled: false,
			Model:         "custom-model",
			Temperature:   0.8,
		}
		globalCfg := &config.AIConfig{DefaultModel: "default-model", Temperature: 0.5}
		engine, err := NewDecisionEngine(mockSvc, cfg, globalCfg)
		if err != nil {
			t.Fatalf("NewDecisionEngine failed: %v", err)
		}

		decisionCtx := &DecisionContext{
			RoomID:           id.RoomID("!test:example.org"),
			ActivityLevel:    ActivityLow,
			MinutesSinceLast: 120,
			MessagesToday:    0,
		}

		shouldSpeak, content, err := engine.Decide(context.Background(), decisionCtx)
		if err != nil {
			t.Errorf("Decide returned error: %v", err)
		}
		if !shouldSpeak {
			t.Error("shouldSpeak should be true")
		}
		if content != "Hi!" {
			t.Errorf("content = %q, want %q", content, "Hi!")
		}
	})

	t.Run("with default model fallback", func(t *testing.T) {
		jsonResponse := `{"should_speak": true, "reason": "test", "content": "Hello!"}`
		mockSvc := &mockAIServiceForDecide{
			enabled:        true,
			simpleResponse: jsonResponse,
		}
		cfg := &config.DecisionConfig{
			StreamEnabled: false,
			Model:         "", // empty, should use default
			Temperature:   0,  // zero, should use default
		}
		globalCfg := &config.AIConfig{DefaultModel: "fallback-model", Temperature: 0.7}
		engine, err := NewDecisionEngine(mockSvc, cfg, globalCfg)
		if err != nil {
			t.Fatalf("NewDecisionEngine failed: %v", err)
		}

		decisionCtx := &DecisionContext{
			RoomID:           id.RoomID("!test:example.org"),
			ActivityLevel:    ActivityLow,
			MinutesSinceLast: 120,
			MessagesToday:    0,
		}

		shouldSpeak, _, err := engine.Decide(context.Background(), decisionCtx)
		if err != nil {
			t.Errorf("Decide returned error: %v", err)
		}
		if !shouldSpeak {
			t.Error("shouldSpeak should be true")
		}
	})
}
