package ai

import (
	"context"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

// SmartStreamHandler 实现了智能流处理逻辑，具有双触发机制（字符阈值和时间阈值）。
//
// 它根据配置的阈值决定何时开始编辑消息，并在后续接收到数据块时更新编辑内容。
type SmartStreamHandler struct {
	editor            *StreamEditor
	charThreshold     int
	timeThreshold     time.Duration
	startTime         time.Time
	hasStartedEditing bool
	mu                sync.Mutex
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

	if !h.hasStartedEditing {
		// 检查是否达到字符阈值或时间阈值
		elapsed := time.Since(h.startTime)
		if len(chunk) >= h.charThreshold || elapsed >= h.timeThreshold {
			// 启动编辑
			h.editor.Start(ctx)
			h.hasStartedEditing = true
		}
	} else {
		// 已经开始编辑，直接更新内容
		h.editor.Update(ctx, chunk)
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

	if !h.hasStartedEditing {
		// 未开始编辑，直接发送最终内容
		h.editor.Start(ctx)
		h.hasStartedEditing = true
	}

	// 使用最终内容更新编辑
	h.editor.Update(ctx, finalContent)
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

	// 停止编辑器
	h.editor.Stop()

	// 发送错误消息（通过 MessageSender 接口）
	errorMsg := "❌ AI 服务出错：" + err.Error()
	h.editor.matrixService.SendText(ctx, h.editor.roomID, errorMsg)
}
