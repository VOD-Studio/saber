// Package agent 提供不依赖聊天平台的单次 Agent 运行时。
package agent

import (
	"context"
	"errors"
	"time"

	"github.com/sashabaranov/go-openai"
)

// Status 表示运行的唯一终态。
type Status string

const (
	// Completed 表示模型正常返回最终回答。
	Completed Status = "completed"
	// Cancelled 表示调用方取消运行。
	Cancelled Status = "cancelled"
	// TimedOut 表示运行的截止时间已到。
	TimedOut Status = "timed_out"
	// BudgetExhausted 表示模型轮数已用完。
	BudgetExhausted Status = "budget_exhausted"
	// Failed 表示模型或运行时发生不可恢复的错误。
	Failed Status = "failed"
)

// ErrBudgetExhausted 允许调用方识别轮数耗尽，避免重跑整个任务。
var ErrBudgetExhausted = errors.New("agent round budget exhausted")

// Request 保留现有模型协议，供流式和非流式 adapter 共用。
type Request struct {
	// Messages 是当前轮的对话历史；adapter 不得修改其中的消息。
	Messages []openai.ChatCompletionMessage `json:"messages"`
	// Stream 选择模型传输方式，不改变运行规则。
	Stream bool `json:"stream"`
	// MaxTokens 限制单次模型生成长度。
	MaxTokens int `json:"max_tokens"`
	// Temperature 控制模型采样。
	Temperature float64 `json:"temperature"`
	// Model 是模型注册表中的标识。
	Model string `json:"model"`
	// Tools 是本次运行允许调用的工具快照。
	Tools []openai.Tool `json:"tools,omitempty"`
	// ToolChoice 保留调用方指定的工具选择策略。
	ToolChoice *string `json:"tool_choice,omitempty"`
}

// Response 是一轮完整响应；失败时也可返回已收到的部分响应用于记录。
type Response struct {
	// Content 是模型返回的文本。
	Content string `json:"content"`
	// Usage 是模型报告的用量，未报告时为零。
	Usage openai.Usage `json:"usage"`
	// Model 是实际响应模型。
	Model string `json:"model"`
	// ToolCalls 按模型给出的顺序排列。
	ToolCalls []openai.ToolCall `json:"tool_calls,omitempty"`
	// FinishReason 用于拒绝截断或不完整的工具调用。
	FinishReason string `json:"finish_reason"`
}

// Limits 控制一次运行；零值使用 5 轮、120 秒、32 KiB 输出限制。
type Limits struct {
	// MaxRounds 包含首次请求和最终回答；最后一轮不再派发工具。
	MaxRounds int
	// Timeout 覆盖模型、重试等待和工具执行。
	Timeout time.Duration
	// MaxToolOutputBytes 限制每条工具消息，包含截断标记，最小为 128。
	MaxToolOutputBytes int
}

// ToolOutput 保留工具返回的数据及协议级失败标记。
type ToolOutput struct {
	// Value 是待序列化的工具结果。
	Value any
	// IsError 表示工具协议报告了失败，即使 Go error 为空。
	IsError bool
}

// ModelFunc 取得一轮响应，可通过 emit 发出文本和尝试事件。
// 必须遵守 ctx；emit 必须在当前调用内串行调用，返回后不得继续调用。
type ModelFunc func(context.Context, Request, func(Event)) (Response, error)

// ToolFunc 执行一个工具，必须遵守 ctx；运行时不会自动重试工具。
type ToolFunc func(context.Context, string, map[string]any) (ToolOutput, error)

// Attempt 记录一次模型请求尝试，包括失败时的部分响应。
type Attempt struct {
	// Number 是当前轮内从 1 开始的尝试编号。
	Number int
	// Response 是本次尝试的完整或部分响应。
	Response Response
	// Error 保留本次尝试失败原因。
	Error string
	// Duration 是本次尝试耗时。
	Duration time.Duration
}

// ToolRecord 保存模型实际收到的工具结果，避免另存无限长原始输出。
type ToolRecord struct {
	// Call 包含调用 ID、名称和原始参数。
	Call openai.ToolCall
	// Content 与追加到模型历史的工具消息完全一致。
	Content string
	// ErrorCode 为空表示成功，否则描述参数、执行或序列化失败。
	ErrorCode string
	// OriginalBytes 是截断前的内容字节数。
	OriginalBytes int
	// Truncated 表示结果被长度限制截断。
	Truncated bool
	// Duration 是解析和执行耗时。
	Duration time.Duration
}

// Round 记录一轮模型响应和随后执行的工具。
type Round struct {
	// Response 是模型最终返回的完整或部分响应。
	Response Response
	// Attempts 保留 adapter 报告的请求尝试。
	Attempts []Attempt
	// Tools 按执行顺序保存工具记录。
	Tools []ToolRecord
	// Error 是本轮导致运行终止的错误。
	Error string
}

// Result 即使在运行失败时也返回已完成的记录。
type Result struct {
	// Status 是唯一终态。
	Status Status
	// Content 仅在正常完成时包含最终回答。
	Content string
	// Rounds 包含所有已发起的模型轮次。
	Rounds []Round
	// Usage 累计所有已报告尝试的用量。
	Usage openai.Usage
	// Duration 是运行总耗时。
	Duration time.Duration
}

// EventKind 表示可由聊天 adapter 或测试消费的运行事件。
type EventKind string

const (
	// RunStarted 表示运行开始。
	RunStarted EventKind = "run_started"
	// ModelStarted 表示新一轮开始。
	ModelStarted EventKind = "model_started"
	// AttemptStarted 表示模型重试或备用模型开始，展示端应重置该轮临时文本。
	AttemptStarted EventKind = "attempt_started"
	// TextDelta 表示当前尝试的文本增量。
	TextDelta EventKind = "text_delta"
	// AttemptFinished 表示一次模型请求尝试结束。
	AttemptFinished EventKind = "attempt_finished"
	// ModelCompleted 表示当前轮响应已接收，仍须检查协议和预算。
	ModelCompleted EventKind = "model_completed"
	// ToolStarted 表示准备处理工具调用，回调后仍会检查取消。
	ToolStarted EventKind = "tool_started"
	// ToolFinished 表示工具结果已记录。
	ToolFinished EventKind = "tool_finished"
	// RunFinished 表示唯一终态，之后不再产生事件。
	RunFinished EventKind = "run_finished"
)

// Event 由 Run 串行发送；回调应快速返回，不得修改引用的响应数据。
// 完整参数和结果可能包含隐私，持久化消费者应自行实施访问控制。
type Event struct {
	// Kind 是事件类型。
	Kind EventKind
	// Round 是从 1 开始的轮次，运行开始时为 0。
	Round int
	// Text 是文本增量或最终回答。
	Text string
	// Response 用于模型完成事件。
	Response Response
	// Attempt 用于模型尝试事件。
	Attempt Attempt
	// Tool 用于工具开始和完成事件。
	Tool ToolRecord
	// Status 仅在运行结束时设置。
	Status Status
	// Error 是运行终止原因。
	Error error
}
