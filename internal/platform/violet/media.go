package violetplatform

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// maxInboundImageBytes 是入站图片下载的字节上限。
//
// violet 的 /uploads/* 公开读取，但 SSE 里的 media.url 仍不可全信：
// 上限既防误传大文件撑爆内存，也兜一道异常响应。
const maxInboundImageBytes = 10 << 20

// encodeImageAsDataURL 把图片字节编码为 data URL，供 chat.Attachment.URL 使用。
//
// 模型多模态入口接受 data URL；与 matrix 平台的 encodeImageAsDataURL 同形态，
// 此处平台自治保留一份，避免跨包依赖 matrix 内部工具。
func encodeImageAsDataURL(data []byte, mimeType string) string {
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded)
}

// resolveMediaURL 把消息内的 media.url 解析为可下载的绝对 URL。
//
// violet 的 /uploads/* 是站内相对路径（相对站点根），需要拼 endpoint；
// 若已是绝对 URL，必须校验 host 与 endpoint 一致，防止 SSE 伪造外部地址引发 SSRF。
func resolveMediaURL(endpoint, mediaURL string) (string, error) {
	mediaURL = strings.TrimSpace(mediaURL)
	if mediaURL == "" {
		return "", fmt.Errorf("violet media.url 为空")
	}
	parsed, err := url.Parse(mediaURL)
	if err != nil {
		return "", fmt.Errorf("解析 violet media.url 失败: %w", err)
	}
	if parsed.IsAbs() {
		endpointHost := endpointHost(endpoint)
		if parsed.Host != endpointHost {
			return "", fmt.Errorf("violet media.url host %q 与 endpoint %q 不一致，拒绝下载", parsed.Host, endpointHost)
		}
		return mediaURL, nil
	}
	// 相对路径：拼到 endpoint 根（不含 apiPrefix）。
	base := strings.TrimSuffix(endpoint, "/")
	if !strings.HasPrefix(mediaURL, "/") {
		mediaURL = "/" + mediaURL
	}
	return base + mediaURL, nil
}

// endpointHost 取 endpoint 的 host 部分，用于绝对 URL 的 host 校验。
func endpointHost(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		// endpoint 已由 config 校验过协议前缀，走到这里属异常，回退原值便于排查。
		return endpoint
	}
	return parsed.Host
}
