// Package matrix 提供基于 mautrix-go 的 Matrix 客户端封装。
package matrix

import (
	"context"
	"encoding/base64"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// MediaInfo 表示从 Matrix 消息中提取的媒体信息。
// 它封装了媒体类型、URL、加密信息和元数据。
type MediaInfo struct {
	// Type 媒体类型：image、video、audio、file
	Type string
	// URL 媒体内容的 Matrix 内容 URI
	URL id.ContentURIString
	// File 加密文件信息（如果启用了 E2EE）
	File *event.EncryptedFileInfo
	// Info 文件元数据（大小、持续时间等）
	Info *event.FileInfo
	// Body 媒体描述文本
	Body string
	// MimeType 媒体 MIME 类型
	MimeType string
}

// MediaService 提供媒体文件处理和下载服务。
// 它封装了 mautrix 客户端并提供大小限制以防止下载过大的文件。
type MediaService struct {
	client  *mautrix.Client
	maxSize int64
}

// NewMediaService 创建一个新的媒体服务。
// client 用于下载媒体内容。
// maxSize 限制允许下载的最大文件大小（字节），0 表示无限制。
func NewMediaService(client *mautrix.Client, maxSize int64) *MediaService {
	return &MediaService{
		client:  client,
		maxSize: maxSize,
	}
}

// ExtractMediaInfo 从消息内容中提取媒体信息。
// 它处理 MsgImage、MsgVideo、MsgAudio 和 MsgFile 类型。
// 如果内容不是媒体类型，返回 nil。
func ExtractMediaInfo(content *event.MessageEventContent) *MediaInfo {
	if content == nil {
		return nil
	}

	var mediaType string
	switch content.MsgType {
	case event.MsgImage:
		mediaType = "image"
	case event.MsgVideo:
		mediaType = "video"
	case event.MsgAudio:
		mediaType = "audio"
	case event.MsgFile:
		mediaType = "file"
	default:
		return nil
	}

	return &MediaInfo{
		Type:     mediaType,
		URL:      content.URL,
		File:     content.File,
		Info:     content.Info,
		Body:     content.Body,
		MimeType: getMimeType(content),
	}
}

// getMimeType 从消息内容中获取 MIME 类型。
// 它优先从 Info 字段读取，如果不可用则返回空字符串。
func getMimeType(content *event.MessageEventContent) string {
	if content.Info != nil && content.Info.MimeType != "" {
		return content.Info.MimeType
	}
	return ""
}

// GetMXCURI 获取媒体的 MXC URI。
// 优先返回加密文件的 URL，其次返回未加密的 URL。
//
// 返回值:
//   - id.ContentURIString: MXC URI
//   - bool: 是否为加密文件
func (m *MediaInfo) GetMXCURI() (id.ContentURIString, bool) {
	if m.File != nil && m.File.URL != "" {
		return m.File.URL, true
	}
	return m.URL, false
}

// GetMimeType 获取媒体的 MIME 类型。
// 如果 MimeType 字段为空，则从 Info 中读取。
// 如果仍然为空，根据 Type 返回默认值。
//
// 返回值:
//   - string: MIME 类型
func (m *MediaInfo) GetMimeType() string {
	if m.MimeType != "" {
		return m.MimeType
	}
	if m.Info != nil && m.Info.MimeType != "" {
		return m.Info.MimeType
	}
	// 根据 Type 返回默认 MIME 类型
	switch m.Type {
	case "image":
		return "image/jpeg"
	case "video":
		return "video/mp4"
	case "audio":
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
}

// GetSize 获取媒体文件大小（字节）。
//
// 返回值:
//   - int: 文件大小，如果未知返回 0
func (m *MediaInfo) GetSize() int {
	if m.Info != nil {
		return m.Info.Size
	}
	return 0
}

// ValidateSize 验证媒体文件大小是否在限制范围内。
//
// 参数:
//   - maxSize: 最大允许的文件大小（字节）
//
// 返回值:
//   - error: 如果超过限制则返回错误，否则返回 nil
func (m *MediaInfo) ValidateSize(maxSize int64) error {
	if maxSize <= 0 {
		return nil // 无限制
	}
	size := m.GetSize()
	if size > 0 && int64(size) > maxSize {
		return fmt.Errorf("媒体文件大小 %d 字节超过限制 %d 字节", size, maxSize)
	}
	return nil
}

// encodeImageAsDataURL 将图片数据编码为 Base64 Data URL 格式。
// 格式：data:{mimeType};base64,{encodedData}
//
// 参数:
//   - data: 图片原始字节数据
//   - mimeType: MIME 类型，如 "image/jpeg"，为空时默认使用 "image/jpeg"
//
// 返回值:
//   - string: Data URL 格式的字符串
func encodeImageAsDataURL(data []byte, mimeType string) string {
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)
}

// DownloadImage 下载图片并返回 Base64 Data URL 格式字符串。
// 支持加密和非加密图片，会检查文件大小限制。
//
// 参数:
//   - ctx: 上下文
//   - info: 图片媒体信息
//
// 返回值:
//   - string: Base64 Data URL 格式的图片数据
//   - error: 下载或解密失败时返回错误
func (s *MediaService) DownloadImage(ctx context.Context, info *MediaInfo) (string, error) {
	// 1. 获取 MXC URI
	mxcURI, isEncrypted := info.GetMXCURI()

	// 2. 解析 URI
	parsedURI, err := mxcURI.Parse()
	if err != nil {
		return "", fmt.Errorf("解析 MXC URI 失败：%w", err)
	}

	// 3. 检查文件大小限制
	if err := info.ValidateSize(s.maxSize); err != nil {
		return "", err
	}

	// 4. 下载数据
	data, err := s.client.DownloadBytes(ctx, parsedURI)
	if err != nil {
		return "", fmt.Errorf("下载图片失败：%w", err)
	}

	// 5. 如果是加密文件，解密
	if isEncrypted && info.File != nil {
		if err := info.File.DecryptInPlace(data); err != nil {
			return "", fmt.Errorf("解密失败：%w", err)
		}
	}

	// 6. 编码为 Base64 Data URL
	return encodeImageAsDataURL(data, info.GetMimeType()), nil
}
