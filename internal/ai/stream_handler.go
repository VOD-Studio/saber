package ai

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

// SmartStreamHandler 实现了智能流处理逻辑，具有双触发机制（字符阈值和时间阈值）。
//
// 它根据配置的阈值决定何时开始编辑消息，并在后续接收到数据块时更新编辑内容。
type SmartStreamHandler struct {
	editor             *StreamEditor
	charThreshold      int
	timeThreshold      time.Duration
	startTime          time.Time
	hasStartedEditing  bool
	accumulatedContent string
	mu                 sync.Mutex
}

// NewSmartStreamHandler 创建一个新的智能流处理器实例。
//
// 参数:
//   - editor: 流编辑器实例，用于管理 Matrix 消息的编辑
//   - charThreshold: 触发编辑的字符阈值
//   - timeThresholdMs: 触发编辑的时间阈值（毫秒）
//
// 返回值:
//   - *SmartStreamHandler: 创建的智能流处理器实例
func NewSmartStreamHandler(editor *StreamEditor, charThreshold int, timeThresholdMs int) *SmartStreamHandler {
	return &SmartStreamHandler{
		editor:        editor,
		charThreshold: charThreshold,
		timeThreshold: time.Duration(timeThresholdMs) * time.Millisecond,
		startTime:     time.Now(),
	}
}

// OnChunk 处理接收到的数据块。
//
// 它检查是否已开始编辑，如果没有，则根据字符阈值或时间阈值决定是否启动编辑。
// 如果已经启动编辑，则直接更新编辑内容。
//
// 参数:
//   - ctx: 上下文
//   - chunk: 接收到的数据块
func (h *SmartStreamHandler) OnChunk(ctx context.Context, chunk string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.accumulatedContent += chunk

	if !h.hasStartedEditing {
		elapsed := time.Since(h.startTime)
		if len(h.accumulatedContent) >= h.charThreshold || elapsed >= h.timeThreshold {
			if err := h.editor.Start(ctx); err != nil {
				slog.Error("启动流编辑失败", "error", err)
				return
			}
			h.hasStartedEditing = true
			slog.Debug("流编辑已启动", "accumulated_length", len(h.accumulatedContent), "elapsed_ms", elapsed.Milliseconds())
		}
	} else {
		if err := h.editor.Update(ctx, h.accumulatedContent); err != nil {
			slog.Debug("更新流编辑失败", "error", err)
		}
	}
}

// OnComplete 在流完成时调用。
//
// 如果尚未开始编辑，则直接发送最终内容。
// 如果已经开始编辑，则使用最终内容更新编辑。
//
// 参数:
//   - ctx: 上下文
//   - finalContent: 最终生成的内容
//   - usage: 使用统计信息
//   - model: 使用的模型
func (h *SmartStreamHandler) OnComplete(ctx context.Context, finalContent string, usage openai.Usage, model string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	slog.Debug("流式响应完成", "content_length", len(finalContent), "has_started_editing", h.hasStartedEditing)

	if !h.hasStartedEditing {
		if err := h.editor.Start(ctx); err != nil {
			slog.Error("启动流编辑失败", "error", err)
			return
		}
		h.hasStartedEditing = true
	}

	if err := h.editor.SendFinal(ctx, finalContent); err != nil {
		slog.Error("发送最终内容失败", "error", err)
	} else {
		slog.Debug("最终消息发送成功")
	}
}

// OnError 在发生错误时调用。
//
// 它停止流编辑器并向房间发送错误消息。
//
// 参数:
//   - ctx: 上下文
//   - err: 发生的错误
func (h *SmartStreamHandler) OnError(ctx context.Context, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	slog.Error("AI流式响应错误", "error", err)
	h.editor.Stop()

	errorMsg := "❌ AI 服务出错：" + err.Error()
	if sendErr := h.editor.matrixService.SendText(ctx, h.editor.roomID, errorMsg); sendErr != nil {
		slog.Error("发送错误消息失败", "error", sendErr)
	}
}
