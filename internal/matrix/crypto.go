// Package matrix 提供 Saber 机器人的 Matrix 客户端功能。
// 本文件包含端到端加密（E2EE）服务的接口定义。
package matrix

import (
	"context"

	"maunium.net/go/mautrix/event"
)

// CryptoService 定义端到端加密服务的接口。
//
// 该接口允许在不同的加密实现之间切换，包括：
//   - NoopCryptoService：E2EE 禁用模式，直接传递原始事件
//   - OlmCryptoService：完整的 Matrix E2EE 实现（未来）
type CryptoService interface {
	// Init 初始化加密服务。
	// 在 OlmCryptoService 中，这会加载或创建 Olm 账户并恢复会话。
	Init(ctx context.Context) error

	// Decrypt 解密加密事件。
	// 对于加密事件（m.room.encrypted），返回解密后的事件。
	// 对于普通事件，直接返回原事件。
	// 在 NoopCryptoService 中，直接返回输入事件。
	Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error)

	// IsEnabled 返回加密服务是否启用。
	// 用于判断是否需要处理加密事件。
	IsEnabled() bool
}

// NoopCryptoService 是 CryptoService 的空实现，用于 E2EE 禁用模式。
//
// 当配置文件未启用 E2EE 时，使用此实现直接传递原始事件，
// 无需解密处理。这适用于只在不加密的房间中运行的场景。
//
// 零值 NoopCryptoService 是有效的。
type NoopCryptoService struct{}

// NewNoopCryptoService 创建一个空加密服务实例。
//
// 此服务不会执行任何加密或解密操作，适用于 E2EE 禁用模式。
func NewNoopCryptoService() *NoopCryptoService {
	return &NoopCryptoService{}
}

// Init 实现 CryptoService 接口。
//
// 此实现不执行任何操作，始终返回 nil。
func (n *NoopCryptoService) Init(ctx context.Context) error {
	return nil
}

// Decrypt 实现 CryptoService 接口。
//
// 此实现直接返回输入事件，不做任何解密处理。
// 对于需要解密的加密事件，调用者应在调用前检查 IsEnabled()。
func (n *NoopCryptoService) Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error) {
	return evt, nil
}

// IsEnabled 实现 CryptoService 接口。
//
// 此实现始终返回 false，表示加密功能未启用。
func (n *NoopCryptoService) IsEnabled() bool {
	return false
}
