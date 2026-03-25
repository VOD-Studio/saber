//go:build !goolm

// Package matrix 提供 Saber 机器人的 Matrix 客户端功能。
// 本文件包含端到端加密（E2EE）服务的接口定义（存根实现）。
//
// 当未使用 goolm 构建标签时，使用此存根实现。
// E2EE 功能将不可用，但程序可以正常运行。
package matrix

import (
	"context"

	"maunium.net/go/mautrix/event"
)

// CryptoService 定义端到端加密服务的接口。
//
// 该接口允许在不同的加密实现之间切换，包括：
//   - NoopCryptoService：E2EE 禁用模式，直接传递原始事件
type CryptoService interface {
	// Init 初始化加密服务。
	Init(ctx context.Context) error

	// Decrypt 解密加密事件。
	// 对于加密事件（m.room.encrypted），返回解密后的事件。
	// 对于普通事件，直接返回原事件。
	Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error)

	// IsEnabled 返回加密服务是否启用。
	IsEnabled() bool
}

// NoopCryptoService 是 CryptoService 的空实现，用于 E2EE 禁用模式。
//
// 当配置文件未启用 E2EE 或未使用 goolm 构建标签时，
// 使用此实现直接传递原始事件，无需解密处理。
//
// 零值 NoopCryptoService 是有效的。
type NoopCryptoService struct{}

// NewNoopCryptoService 创建一个空加密服务实例。
//
// 此服务不会执行任何加密或解密操作，适用于 E2EE 禁用模式。
func NewNoopCryptoService() *NoopCryptoService {
	return &NoopCryptoService{}
}

// OlmCryptoService 在非 goolm 构建中不可用。
// 使用 goolm 构建标签启用完整的 E2EE 支持。
type OlmCryptoService struct{}

// NewOlmCryptoService 在非 goolm 构建中返回 nil。
// 要启用 E2EE 功能，请使用 goolm 构建标签重新编译。
func NewOlmCryptoService(client any, sessionPath string, pickleKey []byte) *OlmCryptoService {
	return nil
}

// Init 实现 CryptoService 接口。
func (o *OlmCryptoService) Init(ctx context.Context) error {
	return nil
}

// Decrypt 实现 CryptoService 接口。
func (o *OlmCryptoService) Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error) {
	return evt, nil
}

// IsEnabled 实现 CryptoService 接口。
func (o *OlmCryptoService) IsEnabled() bool {
	return false
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
func (n *NoopCryptoService) Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error) {
	return evt, nil
}

// IsEnabled 实现 CryptoService 接口。
//
// 此实现始终返回 false，表示加密功能未启用。
func (n *NoopCryptoService) IsEnabled() bool {
	return false
}
