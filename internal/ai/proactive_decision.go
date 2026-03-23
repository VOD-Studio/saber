// Package ai 提供与 AI 相关的功能，包括主动聊天决策解析。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"text/template"
	"time"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// TriggerType 表示触发主动聊天的原因类型。
type TriggerType string

const (
	// TriggerInactivity 表示因长时间无活动而触发。
	TriggerInactivity TriggerType = "inactivity"
	// TriggerNewUser 表示因新用户加入而触发。
	TriggerNewUser TriggerType = "new_user"
	// TriggerTopicChange 表示因话题变更而触发。
	TriggerTopicChange TriggerType = "topic_change"
	// TriggerScheduled 表示因定时任务而触发。
	TriggerScheduled TriggerType = "scheduled"
	// TriggerManual 表示因手动触发而触发。
	TriggerManual TriggerType = "manual"
)

// ActivityLevel 表示房间活动水平。
type ActivityLevel string

const (
	// ActivityLow 表示低活动水平（消息较少）。
	ActivityLow ActivityLevel = "low"
	// ActivityMedium 表示中等活动水平。
	ActivityMedium ActivityLevel = "medium"
	// ActivityHigh 表示高活动水平（消息频繁）。
	ActivityHigh ActivityLevel = "high"
)

// DecisionContext 封装 AI 主动聊天决策所需的上下文信息。
//
// 它聚合来自多个来源的数据，包括房间状态、活动历史和房间元数据。
type DecisionContext struct {
	// RoomID 是房间的唯一标识符。
	RoomID id.RoomID
	// RoomName 是房间的显示名称。
	RoomName string
	// ActivityLevel 是当前房间的活动水平（"low", "medium", "high"）。
	ActivityLevel ActivityLevel
	// MinutesSinceLast 是距离最后一次用户消息的分钟数。
	MinutesSinceLast int
	// MessagesToday 是今日已发送的主动消息数量。
	MessagesToday int
	// TriggerType 是触发此次决策的原因类型。
	TriggerType TriggerType
	// MemberCount 是房间成员数量。
	MemberCount int
	// IsDirect 表示是否为私聊房间（成员数为2）。
	// 用于区分消息语气：私聊使用亲密个人化语气，群聊使用群体语气。
	IsDirect bool
	// IsEncrypted 表示房间是否启用了端到端加密。
	IsEncrypted bool
	// SilenceThresholdMinutes 是静默检测的阈值（分钟）。
	// 用于判断是否应该发送消息，避免在阈值时间内干扰对话。
	SilenceThresholdMinutes int
}

// RoomStateProvider 定义获取房间状态的接口。
//
// 此接口用于解耦 StateTracker 和决策上下文收集逻辑。
type RoomStateProvider interface {
	GetState(roomID id.RoomID) *RoomState
}

// RoomInfoProvider 定义获取房间信息的接口。
//
// 此接口用于解耦 RoomService 和决策上下文收集逻辑。
type RoomInfoProvider interface {
	GetRoomInfo(ctx context.Context, roomID string) (*matrix.RoomInfo, error)
}

// GatherDecisionContext 收集指定房间的 AI 决策上下文。
//
// 它从 StateTracker 获取状态信息，从 RoomService 获取房间元数据，
// 并计算活动水平等派生指标。
//
// 参数:
//   - ctx: 用于取消和超时的上下文
//   - roomID: 要收集上下文的房间 ID
//   - stateProvider: 提供房间状态的接口
//   - roomInfoProvider: 提供房间信息的接口
//   - triggerType: 触发此次决策的原因
//   - silenceThresholdMinutes: 静默检测阈值（分钟），用于决策判断
//
// 返回:
//   - *DecisionContext: 填充完整的决策上下文
//   - error: 如果获取房间信息失败则返回错误
func GatherDecisionContext(
	ctx context.Context,
	roomID id.RoomID,
	stateProvider RoomStateProvider,
	roomInfoProvider RoomInfoProvider,
	triggerType TriggerType,
	silenceThresholdMinutes int,
) (*DecisionContext, error) {
	// 从状态跟踪器获取房间状态
	state := stateProvider.GetState(roomID)

	// 计算距离最后一条消息的分钟数
	// 如果最后消息时间为零值（从未有用户消息），使用 threshold + 1 与触发器逻辑保持一致
	var minutesSinceLast int
	if state.LastMessageTime.IsZero() {
		minutesSinceLast = silenceThresholdMinutes + 1
	} else {
		minutesSinceLast = calculateMinutesSinceLast(state.LastMessageTime)
	}

	// 从房间服务获取房间元数据
	roomInfo, err := roomInfoProvider.GetRoomInfo(ctx, roomID.String())
	if err != nil {
		// 如果获取房间信息失败，使用默认值继续
		roomInfo = &matrix.RoomInfo{
			ID:          roomID,
			Name:        roomID.String(),
			MemberCount: 0,
			IsEncrypted: false,
		}
	}

	// 计算活动水平
	activityLevel := calculateActivityLevel(state.LastMessageTime, state.MessagesToday)

	// 判断是否为私聊房间（成员数为2）
	isDirect := roomInfo.MemberCount == 2

	return &DecisionContext{
		RoomID:                  roomID,
		RoomName:                roomInfo.Name,
		ActivityLevel:           activityLevel,
		MinutesSinceLast:        minutesSinceLast,
		MessagesToday:           state.MessagesToday,
		TriggerType:             triggerType,
		MemberCount:             roomInfo.MemberCount,
		IsDirect:                isDirect,
		IsEncrypted:             roomInfo.IsEncrypted,
		SilenceThresholdMinutes: silenceThresholdMinutes,
	}, nil
}

// calculateMinutesSinceLast 计算从给定时间到现在的分钟数。
//
// 如果给定时间是零值，则返回 -1 表示未知。
func calculateMinutesSinceLast(lastMessageTime time.Time) int {
	if lastMessageTime.IsZero() {
		return -1
	}

	duration := time.Since(lastMessageTime)
	return int(duration.Minutes())
}

// calculateActivityLevel 根据消息历史计算房间活动水平。
//
// 活动水平的判断标准:
//   - high:   今日消息数 >= 10 或最后消息在 30 分钟内
//   - medium: 今日消息数 >= 3 或最后消息在 2 小时内
//   - low:    其他情况
func calculateActivityLevel(lastMessageTime time.Time, messagesToday int) ActivityLevel {
	// 如果今日消息数较多，直接判定为高活动
	if messagesToday >= 10 {
		return ActivityHigh
	}

	// 如果最后消息时间未知，默认为低活动
	if lastMessageTime.IsZero() {
		return ActivityLow
	}

	// 根据最后消息时间判断
	duration := time.Since(lastMessageTime)

	// 30 分钟内有消息 -> 高活动
	if duration < 30*time.Minute {
		return ActivityHigh
	}

	// 2 小时内有消息 -> 中活动
	if duration < 2*time.Hour {
		return ActivityMedium
	}

	// 今日消息数 >= 3 -> 中活动
	if messagesToday >= 3 {
		return ActivityMedium
	}

	// 其他情况 -> 低活动
	return ActivityLow
}

// GetActivityLevel 返回决策上下文的活动水平。
//
// 此方法提供对 ActivityLevel 字段的直接访问。
func (dc *DecisionContext) GetActivityLevel() ActivityLevel {
	return dc.ActivityLevel
}

// ShouldPromptAI 基于当前上下文判断是否应该触发 AI 主动聊天。
//
// 判断逻辑:
//   - 如果今日已发送消息数 >= 5，则不触发（避免骚扰）
//   - 如果房间活动水平为高，则不触发（避免打扰活跃对话）
//   - 如果距离最后消息时间 < SilenceThresholdMinutes，则不触发（避免干扰）
//   - 其他情况建议触发
//
// 返回:
//   - bool: true 表示应该触发 AI 主动聊天
func (dc *DecisionContext) ShouldPromptAI() bool {
	// 避免过度发送消息（每日上限）
	if dc.MessagesToday >= 5 {
		return false
	}

	// 高活动水平时不触发
	if dc.ActivityLevel == ActivityHigh {
		return false
	}

	// 在静默阈值时间内时不触发
	threshold := dc.SilenceThresholdMinutes
	if threshold <= 0 {
		threshold = 60 // 默认值
	}
	if dc.MinutesSinceLast < threshold {
		return false
	}

	return true
}

// DecisionResponse 表示 AI 决策响应的结构。
//
// 它包含以下字段：
// - ShouldSpeak: 是否应该发送主动消息
// - Reason: 决策原因说明
// - Content: 要发送的消息内容（仅当 ShouldSpeak 为 true 时有效）
type DecisionResponse struct {
	// ShouldSpeak 表示是否应该发送主动消息。
	// 默认值为 false。
	ShouldSpeak bool `json:"should_speak"`

	// Reason 是 AI 决策的原因说明。
	// 用于解释为什么决定发送或不发送消息。
	Reason string `json:"reason"`

	// Content 是要发送的消息内容。
	// 仅当 ShouldSpeak 为 true 时此字段才有效。
	// 如果为 empty 且 ShouldSpeak 为 true，将生成默认消息。
	Content string `json:"content,omitempty"`
}

// ParseDecisionResponse 解析 AI 返回的 JSON 决策响应。
//
// 该方法处理以下情况：
// 1. 有效的 JSON 格式：正常解析所有字段
// 2. 格式错误的 JSON：返回解析错误
// 3. 缺失字段：使用默认值（ShouldSpeak=false, Reason="", Content=""）
//
// 参数:
//   - jsonString: AI 返回的 JSON 字符串
//
// 返回值:
//   - *DecisionResponse: 解析后的决策响应
//   - error: 解析过程中发生的错误
func ParseDecisionResponse(jsonString string) (*DecisionResponse, error) {
	// 处理空字符串
	if jsonString == "" {
		return &DecisionResponse{
			ShouldSpeak: false,
			Reason:      "AI 返回空响应",
			Content:     "",
		}, nil
	}

	var resp DecisionResponse
	if err := json.Unmarshal([]byte(jsonString), &resp); err != nil {
		return nil, fmt.Errorf("解析决策响应失败：%w", err)
	}

	// 确保 Reason 字段不为空（便于调试）
	if resp.Reason == "" {
		if resp.ShouldSpeak {
			resp.Reason = "AI 决定发送消息"
		} else {
			resp.Reason = "AI 决定保持静默"
		}
	}

	return &resp, nil
}

// defaultDecisionPromptTemplate 是决策提示词的默认模板。
//
// 它定义了 AI 决策助手的系统行为和判断标准。
const defaultDecisionPromptTemplate = `你是一个聊天室活跃度助手。你的任务是判断是否应该主动发送消息来激活聊天室氛围。

请根据以下房间状态信息做出决策：

房间名称：{{.RoomName}}
房间类型：{{if .IsDirect}}私聊{{else}}群聊{{end}}
活动水平：{{.ActivityLevel}}
距离最后消息时间：{{.MinutesSinceLast}} 分钟
静默检测阈值：{{.SilenceThresholdMinutes}} 分钟
今日主动消息数：{{.MessagesToday}}

判断标准：
1. 如果活动水平为"high"，通常不需要发送消息（避免打扰活跃对话）
2. 如果活动水平为"low"或"medium"，且距离最后消息超过静默检测阈值，可以考虑发送消息
3. 如果今日主动消息数 >= 5，不应该发送消息（避免骚扰）
4. 如果距离最后消息时间 < 静默检测阈值，不应该发送消息（避免干扰）

消息语气要求：
{{if .IsDirect}}- 这是私聊，请使用亲密、个人化的语气，像朋友一样交谈，使用"你"而不是"大家"{{else}}- 这是群聊，请使用面向群体的语气，可以使用"大家"、"各位"等称呼{{end}}

请以 JSON 格式返回决策：
{
  "should_speak": true/false,
  "reason": "决策原因说明",
  "content": "要发送的消息内容（仅当 should_speak 为 true 时需要）"
}`

// BuildDecisionPrompt 使用配置的模板构建决策提示词。
//
// 它将 DecisionContext 中的变量代入模板，生成最终的 AI 提示词。
// 如果未配置模板，则使用默认模板。
//
// 参数:
//   - ctx: 决策上下文，包含房间状态和活动信息
//   - cfg: 决策配置，包含可选的 PromptTemplate
//
// 返回值:
//   - string: 构建完成的提示词
//   - error: 模板解析或执行过程中的错误
func BuildDecisionPrompt(ctx *DecisionContext, cfg *config.DecisionConfig) (string, error) {
	// 选择模板（使用配置的模板或默认模板）
	tmplStr := cfg.PromptTemplate
	if tmplStr == "" {
		tmplStr = defaultDecisionPromptTemplate
	}

	// 解析模板
	tmpl, err := template.New("decision").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("解析决策模板失败：%w", err)
	}

	// 执行模板，代入上下文变量
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("执行决策模板失败：%w", err)
	}

	return buf.String(), nil
}

// cachedDecision 存储缓存的决策结果。
type cachedDecision struct {
	// Response 是缓存的决策响应。
	Response *DecisionResponse
	// ExpiresAt 是缓存过期时间。
	ExpiresAt time.Time
	// ContextHash 是生成此决策的上下文哈希值。
	ContextHash string
}

// DecisionCache 提供基于房间 ID 和上下文哈希的决策缓存。
//
// 它使用 TTL（Time-To-Live）机制来自动过期缓存的决策，
// 防止对相同上下文的重复 AI 调用，同时确保过时的决策会被清除。
//
// 线程安全：所有公共方法都使用互斥锁保护，可以安全地在多个 goroutine 中使用。
type DecisionCache struct {
	// mu 保护 cache map 的并发访问。
	mu sync.RWMutex
	// cache 存储房间 ID 到缓存决策的映射。
	cache map[string]*cachedDecision
	// defaultTTL 是缓存条目的默认生存时间。
	defaultTTL time.Duration
}

// NewDecisionCache 创建并返回一个新的决策缓存实例。
//
// 参数:
//   - defaultTTL: 缓存条目的默认生存时间（建议值：5 分钟）
//
// 返回值:
//   - *DecisionCache: 初始化的决策缓存
//
// 示例:
//
//	cache := NewDecisionCache(5 * time.Minute)
func NewDecisionCache(defaultTTL time.Duration) *DecisionCache {
	if defaultTTL <= 0 {
		defaultTTL = 5 * time.Minute
	}

	return &DecisionCache{
		cache:      make(map[string]*cachedDecision),
		defaultTTL: defaultTTL,
	}
}

// computeContextHash 为给定的决策上下文计算哈希值。
//
// 哈希值基于以下字段生成：
// - ActivityLevel: 房间活动水平
// - MinutesSinceLast: 距离最后消息的分钟数
// - MessagesToday: 今日消息数量
// - TriggerType: 触发类型
// - MemberCount: 成员数量（分组到范围）
//
// 这确保相似的上下文会产生相同的哈希值，从而可以复用缓存的决策。
//
// 参数:
//   - dc: 决策上下文
//
// 返回值:
//   - string: 上下文哈希字符串
func computeContextHash(dc *DecisionContext) string {
	// 简单的哈希：组合关键字段
	// 在生产环境中，可以使用 crypto/md5 或 crypto/sha256 生成更可靠的哈希
	var memberRange string
	switch {
	case dc.MemberCount == 0:
		memberRange = "0"
	case dc.MemberCount <= 5:
		memberRange = "1-5"
	case dc.MemberCount <= 20:
		memberRange = "6-20"
	default:
		memberRange = "20+"
	}

	return fmt.Sprintf("%s|%d|%d|%s|%s",
		dc.ActivityLevel,
		dc.MinutesSinceLast,
		dc.MessagesToday,
		dc.TriggerType,
		memberRange)
}

// makeCacheKey 生成缓存键。
//
// 缓存键由房间 ID 和上下文哈希组合而成，确保：
// 1. 不同房间的决策不会相互干扰
// 2. 同一房间不同上下文的决策会被区分
//
// 参数:
//   - roomID: 房间 ID
//   - contextHash: 上下文哈希值
//
// 返回值:
//   - string: 缓存键
func makeCacheKey(roomID id.RoomID, contextHash string) string {
	return fmt.Sprintf("%s:%s", roomID, contextHash)
}

// Get 从缓存中获取指定房间和上下文的决策。
//
// 该方法会检查缓存是否命中以及缓存是否过期。
// 如果缓存已过期，该条目会被自动清除。
//
// 参数:
//   - roomID: 房间 ID
//   - context: 决策上下文（用于计算哈希）
//
// 返回值:
//   - *DecisionResponse: 缓存的决策响应（如果命中且未过期）
//   - bool: true 表示缓存命中且有效
func (c *DecisionCache) Get(roomID id.RoomID, context *DecisionContext) (*DecisionResponse, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	contextHash := computeContextHash(context)
	key := makeCacheKey(roomID, contextHash)

	cached, exists := c.cache[key]
	if !exists {
		return nil, false
	}

	// 检查是否过期
	if time.Now().After(cached.ExpiresAt) {
		// 缓存已过期，不返回
		return nil, false
	}

	return cached.Response, true
}

// Set 将决策结果存入缓存。
//
// 缓存条目会使用默认的 TTL 设置过期时间。
// 如果该房间已有相同上下文的缓存，会被新值覆盖。
//
// 参数:
//   - roomID: 房间 ID
//   - context: 决策上下文（用于计算哈希）
//   - response: 要缓存的决策响应
func (c *DecisionCache) Set(roomID id.RoomID, context *DecisionContext, response *DecisionResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	contextHash := computeContextHash(context)
	key := makeCacheKey(roomID, contextHash)

	c.cache[key] = &cachedDecision{
		Response:    response,
		ExpiresAt:   time.Now().Add(c.defaultTTL),
		ContextHash: contextHash,
	}
}

// SetWithTTL 将决策结果存入缓存，并指定自定义 TTL。
//
// 这允许对特定决策设置不同的过期时间。
// 例如，对于重要的决策可以设置更长的缓存时间。
//
// 参数:
//   - roomID: 房间 ID
//   - context: 决策上下文（用于计算哈希）
//   - response: 要缓存的决策响应
//   - ttl: 自定义生存时间
func (c *DecisionCache) SetWithTTL(roomID id.RoomID, context *DecisionContext, response *DecisionResponse, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	contextHash := computeContextHash(context)
	key := makeCacheKey(roomID, contextHash)

	c.cache[key] = &cachedDecision{
		Response:    response,
		ExpiresAt:   time.Now().Add(ttl),
		ContextHash: contextHash,
	}
}

// Invalidate 使指定房间的缓存失效。
//
// 这会在房间状态发生重大变化时调用，例如：
// - 新用户加入
// - 房间配置变更
// - 手动清除缓存
//
// 参数:
//   - roomID: 要清除缓存的房间 ID
func (c *DecisionCache) Invalidate(roomID id.RoomID) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 删除所有属于该房间的缓存条目
	for key := range c.cache {
		// 缓存键格式为 "roomID:contextHash"
		if strings.HasPrefix(key, roomID.String()+":") {
			delete(c.cache, key)
		}
	}
}

// InvalidateAll 使所有缓存失效。
//
// 这通常在系统关闭或全局配置变更时使用。
func (c *DecisionCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 简单粗暴地清空整个缓存
	c.cache = make(map[string]*cachedDecision)
}

// Count 返回当前缓存中的条目数量。
//
// 此方法主要用于调试和监控。
// 注意：返回的值是瞬时的，可能会立即过时。
//
// 返回值:
//   - int: 缓存条目数量
func (c *DecisionCache) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}

// Cleanup 清理所有过期的缓存条目。
//
// 建议定期调用此方法（例如每分钟一次）以保持缓存整洁。
// 也可以在低峰期自动运行，避免缓存占用过多内存。
func (c *DecisionCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, cached := range c.cache {
		if now.After(cached.ExpiresAt) {
			delete(c.cache, key)
		}
	}
}

// DecisionEngine 是 AI 主动聊天决策引擎。
//
// 它负责根据房间上下文信息，使用 AI 模型判断是否应该发送主动消息，
// 以及生成合适的消息内容。
type DecisionEngine struct {
	// aiService 是 AI 服务实例，用于调用 AI 模型。
	aiService AIService
	// config 是决策配置，包含模型名称、温度和提示词模板。
	config *config.DecisionConfig
	// globalAIConfig 是全局 AI 配置，用于获取默认模型。
	globalAIConfig *config.AIConfig
}

// AIService 定义 AI 服务所需的接口。
//
// 该接口用于解耦 DecisionEngine 和具体的 AI 服务实现。
type AIService interface {
	// IsEnabled 检查 AI 服务是否已启用。
	IsEnabled() bool
	// GenerateSimpleResponse 使用 AI 生成简单的响应。
	GenerateSimpleResponse(ctx context.Context, systemPrompt, userMessage string) (string, error)
	// GenerateSimpleResponseWithModel 使用指定模型生成响应。
	GenerateSimpleResponseWithModel(ctx context.Context, modelName string, temperature float64, systemPrompt, userMessage string) (string, error)
	// GenerateStreamingSimpleResponse 使用流式请求生成响应。
	GenerateStreamingSimpleResponse(ctx context.Context, modelName string, temperature float64, systemPrompt, userMessage string) (string, error)
}

// NewDecisionEngine 创建并返回一个新的决策引擎实例。
//
// 参数:
//   - aiService: AI 服务实例（必须非 nil）
//   - cfg: 决策配置（必须非 nil）
//   - globalCfg: 全局 AI 配置（必须非 nil）
//
// 返回值:
//   - *DecisionEngine: 创建的决策引擎
//   - error: 初始化过程中的错误
func NewDecisionEngine(
	aiService AIService,
	cfg *config.DecisionConfig,
	globalCfg *config.AIConfig,
) (*DecisionEngine, error) {
	if aiService == nil {
		return nil, fmt.Errorf("AI 服务不能为空")
	}

	if cfg == nil {
		return nil, fmt.Errorf("决策配置不能为空")
	}

	if globalCfg == nil {
		return nil, fmt.Errorf("全局 AI 配置不能为空")
	}

	engine := &DecisionEngine{
		aiService:      aiService,
		config:         cfg,
		globalAIConfig: globalCfg,
	}

	slog.Debug("决策引擎初始化完成",
		"model", cfg.Model,
		"temperature", cfg.Temperature)

	return engine, nil
}

// Decide 根据房间上下文做出是否发送主动消息的决策。
//
// 它执行以下步骤：
// 1. 构建决策提示词（使用 BuildDecisionPrompt）
// 2. 根据 StreamEnabled 配置选择流式或非流式请求
// 3. 调用 AI 服务获取决策响应
// 4. 解析 JSON 响应（使用 ParseDecisionResponse）
// 5. 返回决策结果
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - decisionCtx: 决策上下文，包含房间状态和活动信息
//
// 返回值:
//   - shouldSpeak: 是否应该发送主动消息
//   - content: 要发送的消息内容（仅当 shouldSpeak 为 true 时有效）
//   - error: 决策过程中发生的错误
func (e *DecisionEngine) Decide(ctx context.Context, decisionCtx *DecisionContext) (shouldSpeak bool, content string, err error) {
	// 检查 AI 服务是否启用
	if !e.aiService.IsEnabled() {
		slog.Warn("AI 服务未启用，决策引擎返回默认值")
		return false, "", nil
	}

	// 构建决策提示词
	prompt, err := BuildDecisionPrompt(decisionCtx, e.config)
	if err != nil {
		return false, "", fmt.Errorf("构建决策提示词失败：%w", err)
	}

	slog.Debug("决策提示词构建完成", "prompt", prompt)

	// 确定使用的模型
	modelName := e.config.Model
	if modelName == "" {
		modelName = e.globalAIConfig.DefaultModel
	}

	// 确定使用的温度
	temperature := e.config.Temperature
	if temperature == 0 {
		temperature = e.globalAIConfig.Temperature
	}

	// 构建系统提示词
	systemPrompt := "你是一个聊天室活跃度助手。请根据房间状态判断是否应该主动发送消息。只返回 JSON 格式的决策结果。"

	var response string

	// 根据配置选择流式或非流式请求
	if e.config.StreamEnabled {
		slog.Debug("使用流式请求进行AI决策", "model", modelName)
		response, err = e.aiService.GenerateStreamingSimpleResponse(ctx, modelName, temperature, systemPrompt, prompt)
	} else {
		slog.Debug("使用非流式请求进行AI决策", "model", modelName)
		response, err = e.aiService.GenerateSimpleResponseWithModel(ctx, modelName, temperature, systemPrompt, prompt)
	}

	if err != nil {
		return false, "", fmt.Errorf("AI 决策请求失败：%w", err)
	}

	slog.Debug("AI 决策响应", "response", response)

	// 从响应中提取 JSON
	jsonStr := extractJSONFromResponse(response)

	// 解析 AI 响应
	decision, err := ParseDecisionResponse(jsonStr)
	if err != nil {
		return false, "", fmt.Errorf("解析决策响应失败：%w", err)
	}

	slog.Info("AI 决策完成",
		"should_speak", decision.ShouldSpeak,
		"reason", decision.Reason,
		"room_id", decisionCtx.RoomID)

	return decision.ShouldSpeak, decision.Content, nil
}

// extractJSONFromResponse 从 AI 响应中提取 JSON 内容。
//
// AI 可能返回包含 markdown 代码块的响应，此函数用于提取实际的 JSON 内容。
//
// 参数:
//   - response: AI 返回的原始响应
//
// 返回值:
//   - string: 提取的 JSON 字符串
func extractJSONFromResponse(response string) string {
	// 尝试提取 markdown 代码块中的 JSON
	if strings.Contains(response, "```") {
		// 查找代码块的开始和结束
		start := strings.Index(response, "```")
		if start != -1 {
			// 跳过 ```json 或 ``` 标记
			rest := response[start+3:]
			// 跳过语言标记（如 json）
			if newlineIdx := strings.Index(rest, "\n"); newlineIdx != -1 {
				rest = rest[newlineIdx+1:]
			}
			// 查找结束标记
			end := strings.Index(rest, "```")
			if end != -1 {
				return strings.TrimSpace(rest[:end])
			}
		}
	}

	// 如果没有代码块，返回原始响应
	return strings.TrimSpace(response)
}
