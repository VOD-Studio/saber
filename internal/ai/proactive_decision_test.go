package ai

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// mockStateProvider 是 RoomStateProvider 的模拟实现。
type mockStateProvider struct {
	state *RoomState
}

func (m *mockStateProvider) GetState(roomID id.RoomID) *RoomState {
	return m.state
}

// mockRoomInfoProvider 是 RoomInfoProvider 的模拟实现。
type mockRoomInfoProvider struct {
	info *matrix.RoomInfo
	err  error
}

func (m *mockRoomInfoProvider) GetRoomInfo(ctx context.Context, roomID string) (*matrix.RoomInfo, error) {
	return m.info, m.err
}

func TestGatherDecisionContext(t *testing.T) {
	roomID := id.RoomID("!test:example.org")

	tests := []struct {
		name                 string
		state                *RoomState
		roomInfo             *matrix.RoomInfo
		providerErr          error
		triggerType          TriggerType
		wantRoomName         string
		wantMessages         int
		wantTrigger          TriggerType
		wantMinutesSinceLast int
		wantErr              bool
	}{
		{
			name: "正常情况 - 有状态和房间信息",
			state: &RoomState{
				LastMessageTime: time.Now().Add(-30 * time.Minute),
				MessagesToday:   2,
			},
			roomInfo: &matrix.RoomInfo{
				ID:          roomID,
				Name:        "测试房间",
				MemberCount: 5,
				IsEncrypted: true,
			},
			triggerType:          TriggerInactivity,
			wantRoomName:         "测试房间",
			wantMessages:         2,
			wantTrigger:          TriggerInactivity,
			wantMinutesSinceLast: 30,
			wantErr:              false,
		},
		{
			name: "房间信息获取失败 - 使用默认值",
			state: &RoomState{
				LastMessageTime: time.Now().Add(-120 * time.Minute),
				MessagesToday:   1,
			},
			roomInfo:             nil,
			providerErr:          fmt.Errorf("获取房间信息失败"),
			triggerType:          TriggerScheduled,
			wantRoomName:         roomID.String(),
			wantMessages:         1,
			wantTrigger:          TriggerScheduled,
			wantMinutesSinceLast: 120,
			wantErr:              false,
		},
		{
			name: "无历史消息 - 零值时间应返回 threshold + 1",
			state: &RoomState{
				LastMessageTime: time.Time{},
				MessagesToday:   0,
			},
			roomInfo: &matrix.RoomInfo{
				ID:          roomID,
				Name:        "新房间",
				MemberCount: 2,
				IsEncrypted: false,
			},
			triggerType:          TriggerNewUser,
			wantRoomName:         "新房间",
			wantMessages:         0,
			wantTrigger:          TriggerNewUser,
			wantMinutesSinceLast: 61, // threshold (60) + 1
			wantErr:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			stateProvider := &mockStateProvider{state: tt.state}
			roomInfoProvider := &mockRoomInfoProvider{info: tt.roomInfo, err: tt.providerErr}

			dc, err := GatherDecisionContext(ctx, roomID, stateProvider, roomInfoProvider, tt.triggerType, 60)

			if (err != nil) != tt.wantErr {
				t.Errorf("GatherDecisionContext() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if dc.RoomName != tt.wantRoomName {
				t.Errorf("RoomName = %v, want %v", dc.RoomName, tt.wantRoomName)
			}

			if dc.MessagesToday != tt.wantMessages {
				t.Errorf("MessagesToday = %v, want %v", dc.MessagesToday, tt.wantMessages)
			}

			if dc.TriggerType != tt.wantTrigger {
				t.Errorf("TriggerType = %v, want %v", dc.TriggerType, tt.wantTrigger)
			}

			if dc.RoomID != roomID {
				t.Errorf("RoomID = %v, want %v", dc.RoomID, roomID)
			}

			// 验证 MinutesSinceLast，允许 ±1 分钟的误差（因为是动态计算）
			if tt.wantMinutesSinceLast > 0 {
				diff := dc.MinutesSinceLast - tt.wantMinutesSinceLast
				if diff < -1 || diff > 1 {
					t.Errorf("MinutesSinceLast = %v, want approximately %v", dc.MinutesSinceLast, tt.wantMinutesSinceLast)
				}
			}
		})
	}
}

func TestCalculateActivityLevel(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name          string
		lastMsgTime   time.Time
		messagesToday int
		want          ActivityLevel
	}{
		{
			name:          "高活动 - 今日消息数 >= 10",
			lastMsgTime:   now.Add(-2 * time.Hour),
			messagesToday: 10,
			want:          ActivityHigh,
		},
		{
			name:          "高活动 - 30 分钟内有消息",
			lastMsgTime:   now.Add(-15 * time.Minute),
			messagesToday: 2,
			want:          ActivityHigh,
		},
		{
			name:          "中活动 - 2 小时内有消息",
			lastMsgTime:   now.Add(-90 * time.Minute),
			messagesToday: 1,
			want:          ActivityMedium,
		},
		{
			name:          "中活动 - 今日消息数 >= 3",
			lastMsgTime:   now.Add(-3 * time.Hour),
			messagesToday: 5,
			want:          ActivityMedium,
		},
		{
			name:          "低活动 - 其他情况",
			lastMsgTime:   now.Add(-5 * time.Hour),
			messagesToday: 1,
			want:          ActivityLow,
		},
		{
			name:          "低活动 - 零值时间",
			lastMsgTime:   time.Time{},
			messagesToday: 0,
			want:          ActivityLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateActivityLevel(tt.lastMsgTime, tt.messagesToday)
			if got != tt.want {
				t.Errorf("calculateActivityLevel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCalculateMinutesSinceLast(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name          string
		lastMsgTime   time.Time
		wantApproxMin int // 期望的近似分钟数（允许 1 分钟误差）
		wantNegative  bool
	}{
		{
			name:          "60 分钟前",
			lastMsgTime:   now.Add(-60 * time.Minute),
			wantApproxMin: 60,
		},
		{
			name:          "30 分钟前",
			lastMsgTime:   now.Add(-30 * time.Minute),
			wantApproxMin: 30,
		},
		{
			name:          "2 小时前",
			lastMsgTime:   now.Add(-120 * time.Minute),
			wantApproxMin: 120,
		},
		{
			name:         "零值时间",
			lastMsgTime:  time.Time{},
			wantNegative: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateMinutesSinceLast(tt.lastMsgTime)

			if tt.wantNegative {
				if got != -1 {
					t.Errorf("calculateMinutesSinceLast() = %v, want -1 for zero time", got)
				}
				return
			}

			// 允许 1 分钟的误差（因为测试执行需要时间）
			diff := got - tt.wantApproxMin
			if diff < -1 || diff > 1 {
				t.Errorf("calculateMinutesSinceLast() = %v, want approx %v", got, tt.wantApproxMin)
			}
		})
	}
}

func TestDecisionContext_ShouldPromptAI(t *testing.T) {
	tests := []struct {
		name       string
		context    *DecisionContext
		wantPrompt bool
	}{
		{
			name: "应该触发 - 低活动且超过 60 分钟",
			context: &DecisionContext{
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: 120,
				MessagesToday:    0,
			},
			wantPrompt: true,
		},
		{
			name: "不应该触发 - 今日消息数已达上限",
			context: &DecisionContext{
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: 120,
				MessagesToday:    5,
			},
			wantPrompt: false,
		},
		{
			name: "不应该触发 - 高活动水平",
			context: &DecisionContext{
				ActivityLevel:    ActivityHigh,
				MinutesSinceLast: 120,
				MessagesToday:    0,
			},
			wantPrompt: false,
		},
		{
			name: "不应该触发 - 最近 60 分钟内有消息",
			context: &DecisionContext{
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: 30,
				MessagesToday:    0,
			},
			wantPrompt: false,
		},
		{
			name: "不应该触发 - MinutesSinceLast 为 -1（未知）",
			context: &DecisionContext{
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: -1,
				MessagesToday:    0,
			},
			wantPrompt: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.context.ShouldPromptAI()
			if got != tt.wantPrompt {
				t.Errorf("ShouldPromptAI() = %v, want %v", got, tt.wantPrompt)
			}
		})
	}
}

func TestDecisionContext_GetActivityLevel(t *testing.T) {
	dc := &DecisionContext{
		ActivityLevel: ActivityMedium,
	}

	got := dc.GetActivityLevel()
	if got != ActivityMedium {
		t.Errorf("GetActivityLevel() = %v, want %v", got, ActivityMedium)
	}
}

func TestTriggerTypeConstants(t *testing.T) {
	// 验证 TriggerType 常量定义
	expectedTriggers := map[TriggerType]bool{
		TriggerInactivity:  true,
		TriggerNewUser:     true,
		TriggerTopicChange: true,
		TriggerScheduled:   true,
		TriggerManual:      true,
	}

	for trigger := range expectedTriggers {
		if string(trigger) == "" {
			t.Errorf("TriggerType constant is empty: %v", trigger)
		}
	}
}

func TestActivityLevelConstants(t *testing.T) {
	// 验证 ActivityLevel 常量定义
	expectedLevels := map[ActivityLevel]bool{
		ActivityLow:    true,
		ActivityMedium: true,
		ActivityHigh:   true,
	}

	for _, level := range []ActivityLevel{ActivityLow, ActivityMedium, ActivityHigh} {
		if string(level) == "" {
			t.Errorf("ActivityLevel constant is empty: %v", level)
		}
		if !expectedLevels[level] {
			t.Errorf("Unexpected ActivityLevel: %v", level)
		}
	}
}

func TestParseDecisionResponse_ValidJSON(t *testing.T) {
	tests := []struct {
		name            string
		jsonInput       string
		wantShouldSpeak bool
		wantReason      string
		wantContent     string
		wantErr         bool
	}{
		{
			name:            "完整字段 - 应该说话",
			jsonInput:       `{"should_speak": true, "reason": "检测到用户活跃", "content": "你好！"}`,
			wantShouldSpeak: true,
			wantReason:      "检测到用户活跃",
			wantContent:     "你好！",
			wantErr:         false,
		},
		{
			name:            "完整字段 - 保持静默",
			jsonInput:       `{"should_speak": false, "reason": "用户刚刚活跃", "content": ""}`,
			wantShouldSpeak: false,
			wantReason:      "用户刚刚活跃",
			wantContent:     "",
			wantErr:         false,
		},
		{
			name:            "缺失 content 字段",
			jsonInput:       `{"should_speak": true, "reason": "定时触发"}`,
			wantShouldSpeak: true,
			wantReason:      "定时触发",
			wantContent:     "",
			wantErr:         false,
		},
		{
			name:            "缺失 reason 字段 - ShouldSpeak=true",
			jsonInput:       `{"should_speak": true, "content": "测试消息"}`,
			wantShouldSpeak: true,
			wantReason:      "AI 决定发送消息",
			wantContent:     "测试消息",
			wantErr:         false,
		},
		{
			name:            "缺失 reason 字段 - ShouldSpeak=false",
			jsonInput:       `{"should_speak": false}`,
			wantShouldSpeak: false,
			wantReason:      "AI 决定保持静默",
			wantContent:     "",
			wantErr:         false,
		},
		{
			name:            "空字符串",
			jsonInput:       "",
			wantShouldSpeak: false,
			wantReason:      "AI 返回空响应",
			wantContent:     "",
			wantErr:         false,
		},
		{
			name:            "空 JSON 对象",
			jsonInput:       `{}`,
			wantShouldSpeak: false,
			wantReason:      "AI 决定保持静默",
			wantContent:     "",
			wantErr:         false,
		},
		{
			name:            "格式错误的 JSON",
			jsonInput:       `{invalid json}`,
			wantShouldSpeak: false,
			wantReason:      "",
			wantContent:     "",
			wantErr:         true,
		},
		{
			name:            "JSON 数组（错误类型）",
			jsonInput:       `[]`,
			wantShouldSpeak: false,
			wantReason:      "",
			wantContent:     "",
			wantErr:         true,
		},
		{
			name:            "纯文本（非 JSON）",
			jsonInput:       `这是一条纯文本消息`,
			wantShouldSpeak: false,
			wantReason:      "",
			wantContent:     "",
			wantErr:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDecisionResponse(tt.jsonInput)

			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDecisionResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if got == nil {
					t.Fatal("ParseDecisionResponse() returned nil for valid input")
					return // 让静态分析器明确知道这里会终止
				}
				if got.ShouldSpeak != tt.wantShouldSpeak {
					t.Errorf("ShouldSpeak = %v, want %v", got.ShouldSpeak, tt.wantShouldSpeak)
				}
				if got.Reason != tt.wantReason {
					t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
				}
				if got.Content != tt.wantContent {
					t.Errorf("Content = %q, want %q", got.Content, tt.wantContent)
				}
			}
		})
	}
}

func TestParseDecisionResponse_DefaultReason(t *testing.T) {
	// 测试缺失 reason 字段时的默认值逻辑
	tests := []struct {
		name       string
		jsonInput  string
		wantReason string
		wantSpeak  bool
	}{
		{
			name:       "ShouldSpeak true 时的默认原因",
			jsonInput:  `{"should_speak": true}`,
			wantReason: "AI 决定发送消息",
			wantSpeak:  true,
		},
		{
			name:       "ShouldSpeak false 时的默认原因",
			jsonInput:  `{"should_speak": false}`,
			wantReason: "AI 决定保持静默",
			wantSpeak:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDecisionResponse(tt.jsonInput)
			if err != nil {
				t.Fatalf("ParseDecisionResponse() unexpected error = %v", err)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if got.ShouldSpeak != tt.wantSpeak {
				t.Errorf("ShouldSpeak = %v, want %v", got.ShouldSpeak, tt.wantSpeak)
			}
		})
	}
}

func TestBuildDecisionPrompt(t *testing.T) {
	tests := []struct {
		name        string
		ctx         *DecisionContext
		cfg         *config.DecisionConfig
		wantContain string
		wantErr     bool
	}{
		{
			name: "使用默认模板",
			ctx: &DecisionContext{
				RoomID:           "!test:example.org",
				RoomName:         "测试房间",
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: 120,
				MessagesToday:    2,
				TriggerType:      TriggerInactivity,
				MemberCount:      5,
				IsEncrypted:      true,
			},
			cfg:         &config.DecisionConfig{},
			wantContain: "房间名称：测试房间",
			wantErr:     false,
		},
		{
			name: "使用自定义模板",
			ctx: &DecisionContext{
				RoomID:           "!test:example.org",
				RoomName:         "自定义房间",
				ActivityLevel:    ActivityHigh,
				MinutesSinceLast: 30,
				MessagesToday:    4,
			},
			cfg: &config.DecisionConfig{
				PromptTemplate: "房间：{{.RoomName}}, 活动：{{.ActivityLevel}}",
			},
			wantContain: "房间：自定义房间, 活动：high",
			wantErr:     false,
		},
		{
			name: "模板变量替换 - 所有字段",
			ctx: &DecisionContext{
				RoomName:         "活跃房间",
				ActivityLevel:    ActivityMedium,
				MinutesSinceLast: 90,
				MessagesToday:    3,
			},
			cfg:         &config.DecisionConfig{},
			wantContain: "活动水平：medium",
			wantErr:     false,
		},
		{
			name: "无效模板 - 语法错误",
			ctx: &DecisionContext{
				RoomName: "测试",
			},
			cfg: &config.DecisionConfig{
				PromptTemplate: "{{.Invalid", // 无效的模板语法
			},
			wantErr: true,
		},
		{
			name: "默认模板包含判断标准",
			ctx: &DecisionContext{
				RoomName:         "默认房间",
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: 180,
				MessagesToday:    0,
			},
			cfg:         &config.DecisionConfig{},
			wantContain: "判断标准",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildDecisionPrompt(tt.ctx, tt.cfg)

			if (err != nil) != tt.wantErr {
				t.Errorf("BuildDecisionPrompt() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if tt.wantContain != "" && !strings.Contains(got, tt.wantContain) {
					t.Errorf("BuildDecisionPrompt() result does not contain %q\ngot: %s", tt.wantContain, got)
				}
			}
		})
	}
}

func TestBuildDecisionPrompt_TemplateVariables(t *testing.T) {
	ctx := &DecisionContext{
		RoomName:         "变量测试房间",
		ActivityLevel:    ActivityHigh,
		MinutesSinceLast: 45,
		MessagesToday:    7,
	}

	got, err := BuildDecisionPrompt(ctx, &config.DecisionConfig{})
	if err != nil {
		t.Fatalf("BuildDecisionPrompt() unexpected error = %v", err)
	}

	requiredSubstrings := []string{
		"房间名称：变量测试房间",
		"活动水平：high",
		"距离最后消息时间：45 分钟",
		"今日主动消息数：7",
	}

	for _, substr := range requiredSubstrings {
		if !strings.Contains(got, substr) {
			t.Errorf("BuildDecisionPrompt() result does not contain %q\ngot: %s", substr, got)
		}
	}
}

func TestDecisionCache_NewDecisionCache(t *testing.T) {
	tests := []struct {
		name     string
		ttl      time.Duration
		wantTTL  time.Duration
		wantSize int
	}{
		{
			name:     "正常 TTL",
			ttl:      5 * time.Minute,
			wantTTL:  5 * time.Minute,
			wantSize: 0,
		},
		{
			name:     "零 TTL 使用默认值",
			ttl:      0,
			wantTTL:  5 * time.Minute,
			wantSize: 0,
		},
		{
			name:     "负 TTL 使用默认值",
			ttl:      -1 * time.Minute,
			wantTTL:  5 * time.Minute,
			wantSize: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewDecisionCache(tt.ttl)
			if cache == nil {
				t.Fatal("NewDecisionCache() returned nil")
			}
			if cache.Count() != tt.wantSize {
				t.Errorf("Count() = %d, want %d", cache.Count(), tt.wantSize)
			}
		})
	}
}

func TestDecisionCache_ComputeContextHash(t *testing.T) {
	tests := []struct {
		name       string
		context    *DecisionContext
		wantHash   string
		wantEquals bool // 是否与 wantHash 相等（用于测试相同上下文）
	}{
		{
			name: "相同上下文产生相同哈希",
			context: &DecisionContext{
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: 120,
				MessagesToday:    2,
				TriggerType:      TriggerInactivity,
				MemberCount:      3,
			},
			wantHash:   "low|120|2|inactivity|1-5",
			wantEquals: true,
		},
		{
			name: "不同活动水平产生不同哈希",
			context: &DecisionContext{
				ActivityLevel:    ActivityHigh,
				MinutesSinceLast: 120,
				MessagesToday:    2,
				TriggerType:      TriggerInactivity,
				MemberCount:      3,
			},
			wantHash:   "high|120|2|inactivity|1-5",
			wantEquals: true,
		},
		{
			name: "成员数量分组 - 0",
			context: &DecisionContext{
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: 60,
				MessagesToday:    1,
				TriggerType:      TriggerScheduled,
				MemberCount:      0,
			},
			wantHash:   "low|60|1|scheduled|0",
			wantEquals: true,
		},
		{
			name: "成员数量分组 - 1-5",
			context: &DecisionContext{
				ActivityLevel:    ActivityMedium,
				MinutesSinceLast: 90,
				MessagesToday:    3,
				TriggerType:      TriggerNewUser,
				MemberCount:      5,
			},
			wantHash:   "medium|90|3|new_user|1-5",
			wantEquals: true,
		},
		{
			name: "成员数量分组 - 6-20",
			context: &DecisionContext{
				ActivityLevel:    ActivityHigh,
				MinutesSinceLast: 30,
				MessagesToday:    10,
				TriggerType:      TriggerManual,
				MemberCount:      15,
			},
			wantHash:   "high|30|10|manual|6-20",
			wantEquals: true,
		},
		{
			name: "成员数量分组 - 20+",
			context: &DecisionContext{
				ActivityLevel:    ActivityLow,
				MinutesSinceLast: 180,
				MessagesToday:    0,
				TriggerType:      TriggerInactivity,
				MemberCount:      50,
			},
			wantHash:   "low|180|0|inactivity|20+",
			wantEquals: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeContextHash(tt.context)
			if tt.wantEquals && got != tt.wantHash {
				t.Errorf("computeContextHash() = %q, want %q", got, tt.wantHash)
			}
		})
	}
}

func TestDecisionCache_GetAndSet(t *testing.T) {
	roomID := id.RoomID("!test:example.org")
	cache := NewDecisionCache(5 * time.Minute)

	context := &DecisionContext{
		ActivityLevel:    ActivityLow,
		MinutesSinceLast: 120,
		MessagesToday:    2,
		TriggerType:      TriggerInactivity,
		MemberCount:      3,
		RoomName:         "Test Room",
		IsEncrypted:      false,
	}

	response := &DecisionResponse{
		ShouldSpeak: true,
		Reason:      "测试原因",
		Content:     "测试消息内容",
	}

	// 测试缓存未命中
	if got, ok := cache.Get(roomID, context); ok || got != nil {
		t.Errorf("Get() before Set should return (nil, false), got (%v, %v)", got, ok)
	}

	// 测试设置缓存
	cache.Set(roomID, context, response)

	// 测试缓存命中
	got, ok := cache.Get(roomID, context)
	if !ok {
		t.Fatal("Get() after Set should return (response, true)")
	}
	if got.ShouldSpeak != response.ShouldSpeak {
		t.Errorf("Got ShouldSpeak=%v, want %v", got.ShouldSpeak, response.ShouldSpeak)
	}
	if got.Reason != response.Reason {
		t.Errorf("Got Reason=%q, want %q", got.Reason, response.Reason)
	}
	if got.Content != response.Content {
		t.Errorf("Got Content=%q, want %q", got.Content, response.Content)
	}
}

func TestDecisionCache_Expiration(t *testing.T) {
	roomID := id.RoomID("!test:example.org")
	cache := NewDecisionCache(100 * time.Millisecond)

	context := &DecisionContext{
		ActivityLevel:    ActivityLow,
		MinutesSinceLast: 60,
		MessagesToday:    1,
		TriggerType:      TriggerScheduled,
		MemberCount:      5,
	}

	response := &DecisionResponse{
		ShouldSpeak: false,
		Reason:      "缓存测试",
		Content:     "",
	}

	// 设置缓存
	cache.Set(roomID, context, response)

	// 立即获取应该命中
	if _, ok := cache.Get(roomID, context); !ok {
		t.Fatal("Get() immediately after Set should hit cache")
	}

	// 等待过期
	time.Sleep(150 * time.Millisecond)

	// 获取应该未命中（已过期）
	if got, ok := cache.Get(roomID, context); ok || got != nil {
		t.Errorf("Get() after expiration should return (nil, false), got (%v, %v)", got, ok)
	}
}

func TestDecisionCache_SetWithTTL(t *testing.T) {
	roomID := id.RoomID("!test:example.org")
	cache := NewDecisionCache(5 * time.Minute)

	context := &DecisionContext{
		ActivityLevel:    ActivityMedium,
		MinutesSinceLast: 90,
		MessagesToday:    3,
		TriggerType:      TriggerNewUser,
		MemberCount:      10,
	}

	response := &DecisionResponse{
		ShouldSpeak: true,
		Reason:      "自定义 TTL 测试",
		Content:     "测试",
	}

	// 使用自定义 TTL 设置缓存（100 毫秒）
	cache.SetWithTTL(roomID, context, response, 100*time.Millisecond)

	// 立即获取应该命中
	if _, ok := cache.Get(roomID, context); !ok {
		t.Fatal("Get() immediately after SetWithTTL should hit cache")
	}

	// 等待过期
	time.Sleep(150 * time.Millisecond)

	// 获取应该未命中
	if got, ok := cache.Get(roomID, context); ok || got != nil {
		t.Errorf("Get() after TTL expiration should return (nil, false), got (%v, %v)", got, ok)
	}
}

func TestDecisionCache_Invalidate(t *testing.T) {
	room1 := id.RoomID("!room1:example.org")
	room2 := id.RoomID("!room2:example.org")
	cache := NewDecisionCache(5 * time.Minute)

	context1 := &DecisionContext{
		ActivityLevel:    ActivityLow,
		MinutesSinceLast: 120,
		MessagesToday:    2,
		TriggerType:      TriggerInactivity,
		MemberCount:      3,
	}

	context2 := &DecisionContext{
		ActivityLevel:    ActivityHigh,
		MinutesSinceLast: 30,
		MessagesToday:    10,
		TriggerType:      TriggerScheduled,
		MemberCount:      15,
	}

	response := &DecisionResponse{ShouldSpeak: true, Reason: "测试", Content: "测试"}

	// 为两个房间设置缓存
	cache.Set(room1, context1, response)
	cache.Set(room2, context2, response)

	if cache.Count() != 2 {
		t.Fatalf("Count() = %d, want 2", cache.Count())
	}

	// 使 room1 失效
	cache.Invalidate(room1)

	// room1 应该未命中
	if _, ok := cache.Get(room1, context1); ok {
		t.Error("Get() after Invalidate should miss for room1")
	}

	// room2 应该仍然命中
	if _, ok := cache.Get(room2, context2); !ok {
		t.Error("Get() after Invalidate should still hit for room2")
	}

	if cache.Count() != 1 {
		t.Errorf("Count() = %d, want 1", cache.Count())
	}
}

func TestDecisionCache_InvalidateAll(t *testing.T) {
	cache := NewDecisionCache(5 * time.Minute)

	rooms := []id.RoomID{
		id.RoomID("!room1:example.org"),
		id.RoomID("!room2:example.org"),
		id.RoomID("!room3:example.org"),
	}

	context := &DecisionContext{
		ActivityLevel:    ActivityLow,
		MinutesSinceLast: 120,
		MessagesToday:    2,
		TriggerType:      TriggerInactivity,
		MemberCount:      3,
	}

	response := &DecisionResponse{ShouldSpeak: true, Reason: "测试", Content: "测试"}

	// 为多个房间设置缓存
	for _, roomID := range rooms {
		cache.Set(roomID, context, response)
	}

	if cache.Count() != len(rooms) {
		t.Fatalf("Count() = %d, want %d", cache.Count(), len(rooms))
	}

	// 使所有缓存失效
	cache.InvalidateAll()

	if cache.Count() != 0 {
		t.Errorf("Count() after InvalidateAll = %d, want 0", cache.Count())
	}

	// 验证所有房间都未命中
	for _, roomID := range rooms {
		if _, ok := cache.Get(roomID, context); ok {
			t.Errorf("Get() after InvalidateAll should miss for room %s", roomID)
		}
	}
}

func TestDecisionCache_Cleanup(t *testing.T) {
	cache := NewDecisionCache(5 * time.Minute)

	room1 := id.RoomID("!room1:example.org")
	room2 := id.RoomID("!room2:example.org")

	context := &DecisionContext{
		ActivityLevel:    ActivityLow,
		MinutesSinceLast: 60,
		MessagesToday:    1,
		TriggerType:      TriggerScheduled,
		MemberCount:      5,
	}

	response := &DecisionResponse{ShouldSpeak: false, Reason: "测试", Content: ""}

	// 设置两个缓存，一个使用短 TTL
	cache.Set(room1, context, response)                             // 5 分钟 TTL
	cache.SetWithTTL(room2, context, response, 50*time.Millisecond) // 50ms TTL

	if cache.Count() != 2 {
		t.Fatalf("Count() before cleanup = %d, want 2", cache.Count())
	}

	// 等待短 TTL 过期
	time.Sleep(100 * time.Millisecond)

	// 清理过期条目
	cache.Cleanup()

	if cache.Count() != 1 {
		t.Errorf("Count() after cleanup = %d, want 1", cache.Count())
	}

	// room1 应该仍然在缓存中
	if _, ok := cache.Get(room1, context); !ok {
		t.Error("room1 should still be in cache after cleanup")
	}

	// room2 应该已被清理
	if _, ok := cache.Get(room2, context); ok {
		t.Error("room2 should be removed by cleanup")
	}
}

func TestDecisionCache_DifferentRooms(t *testing.T) {
	cache := NewDecisionCache(5 * time.Minute)

	room1 := id.RoomID("!room1:example.org")
	room2 := id.RoomID("!room2:example.org")

	context := &DecisionContext{
		ActivityLevel:    ActivityLow,
		MinutesSinceLast: 120,
		MessagesToday:    2,
		TriggerType:      TriggerInactivity,
		MemberCount:      3,
	}

	response1 := &DecisionResponse{ShouldSpeak: true, Reason: "房间 1", Content: "消息 1"}
	response2 := &DecisionResponse{ShouldSpeak: false, Reason: "房间 2", Content: "消息 2"}

	// 为不同房间设置不同的响应
	cache.Set(room1, context, response1)
	cache.Set(room2, context, response2)

	// 验证每个房间获取正确的响应
	got1, ok1 := cache.Get(room1, context)
	if !ok1 {
		t.Fatal("Get() for room1 should hit cache")
	}
	if got1.ShouldSpeak != true {
		t.Errorf("Room1 ShouldSpeak = %v, want true", got1.ShouldSpeak)
	}

	got2, ok2 := cache.Get(room2, context)
	if !ok2 {
		t.Fatal("Get() for room2 should hit cache")
	}
	if got2.ShouldSpeak != false {
		t.Errorf("Room2 ShouldSpeak = %v, want false", got2.ShouldSpeak)
	}
}

func TestDecisionCache_ContextHashIsolation(t *testing.T) {
	cache := NewDecisionCache(5 * time.Minute)
	roomID := id.RoomID("!test:example.org")

	context1 := &DecisionContext{
		ActivityLevel:    ActivityLow,
		MinutesSinceLast: 60,
		MessagesToday:    1,
		TriggerType:      TriggerScheduled,
		MemberCount:      5,
	}

	context2 := &DecisionContext{
		ActivityLevel:    ActivityHigh, // 不同的活动水平
		MinutesSinceLast: 60,
		MessagesToday:    1,
		TriggerType:      TriggerScheduled,
		MemberCount:      5,
	}

	response1 := &DecisionResponse{ShouldSpeak: true, Reason: "上下文 1", Content: "消息 1"}
	response2 := &DecisionResponse{ShouldSpeak: false, Reason: "上下文 2", Content: "消息 2"}

	// 为同一房间的不同上下文设置缓存
	cache.Set(roomID, context1, response1)
	cache.Set(roomID, context2, response2)

	// 验证两个上下文都被缓存（计数为 2）
	if cache.Count() != 2 {
		t.Errorf("Count() = %d, want 2 (different contexts should be cached separately)", cache.Count())
	}

	// 验证每个上下文获取正确的响应
	got1, ok1 := cache.Get(roomID, context1)
	if !ok1 {
		t.Fatal("Get() for context1 should hit cache")
	}
	if got1.ShouldSpeak != true {
		t.Errorf("Context1 ShouldSpeak = %v, want true", got1.ShouldSpeak)
	}

	got2, ok2 := cache.Get(roomID, context2)
	if !ok2 {
		t.Fatal("Get() for context2 should hit cache")
	}
	if got2.ShouldSpeak != false {
		t.Errorf("Context2 ShouldSpeak = %v, want false", got2.ShouldSpeak)
	}
}
