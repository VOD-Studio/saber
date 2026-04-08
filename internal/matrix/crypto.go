//go:build goolm

// Package matrix 提供 Saber 机器人的 Matrix 客户端功能。
// 本文件包含端到端加密（E2EE）服务的接口定义。
//
// 使用 goolm（纯 Go 的 libolm 实现），无需 CGO 依赖。
// 构建时请添加 -tags goolm 标志。
package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
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

// OlmCryptoService 是使用 mautrix CryptoHelper 的端到端加密实现。
//
// 它封装了 mautrix 的 cryptohelper.CryptoHelper，提供完整的 Matrix
// 端到端加密功能，包括密钥管理、会话恢复和事件解密。
type OlmCryptoService struct {
	cryptoHelper *cryptohelper.CryptoHelper
	client       *mautrix.Client
	sessionPath  string
	pickleKey    []byte
}

// NewNoopCryptoService 创建一个空加密服务实例。
//
// 此服务不会执行任何加密或解密操作，适用于 E2EE 禁用模式。
func NewNoopCryptoService() *NoopCryptoService {
	return &NoopCryptoService{}
}

// NewOlmCryptoService 创建一个新的端到端加密服务实例。
//
// 参数:
//   - client: mautrix 客户端实例
//   - sessionPath: 加密会话数据库文件路径（SQLite）
//   - pickleKey: 用于加密存储密钥的密钥
//
// 注意：实际的加密初始化在 Init() 方法中进行，而不是在构造函数中。
// 这允许延迟初始化并在适当的上下文中处理错误。
func NewOlmCryptoService(client *mautrix.Client, sessionPath string, pickleKey []byte) *OlmCryptoService {
	return &OlmCryptoService{
		client:      client,
		sessionPath: sessionPath,
		pickleKey:   pickleKey,
	}
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

// Init 实现 CryptoService 接口。
//
// 此方法初始化 mautrix CryptoHelper，创建或加载 Olm 账户，
// 并设置客户端的 Crypto 字段以自动处理加密/解密。
// 它使用配置的会话路径和 pickle 密钥来管理加密存储。
func (o *OlmCryptoService) Init(ctx context.Context) error {
	helper, err := cryptohelper.NewCryptoHelper(o.client, o.pickleKey, o.sessionPath)
	if err != nil {
		return fmt.Errorf("failed to create crypto helper: %w", err)
	}

	if err := helper.Init(ctx); err != nil {
		return fmt.Errorf("failed to initialize crypto helper: %w", err)
	}

	o.cryptoHelper = helper
	o.client.Crypto = helper
	return nil
}

// Decrypt 实现 CryptoService 接口。
//
// 此方法使用 CryptoHelper 解密加密事件。
// 对于非加密事件，此方法的行为未定义，调用者应先检查事件类型。
//
// 集成位置: EventHandler.handleEncryptedEvent
// 当收到 m.room.encrypted 事件时，由 handleEncryptedEvent 调用此方法解密。
// 注意：mautrix 在启用 E2EE 时会自动解密，此方法用于特殊场景或未自动解密的情况。
func (o *OlmCryptoService) Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error) {
	if o.cryptoHelper == nil {
		return nil, fmt.Errorf("crypto helper not initialized")
	}
	return o.cryptoHelper.Decrypt(ctx, evt)
}

// IsEnabled 实现 CryptoService 接口。
//
// 此方法返回加密服务是否已成功初始化。
// 当 cryptoHelper 字段不为 nil 时返回 true，表示服务已启用并可处理加密事件。
func (o *OlmCryptoService) IsEnabled() bool {
	return o.cryptoHelper != nil
}
