package acp

import (
	"fmt"
	"strings"

	"rua.plus/saber/internal/chat"
)

func promptContent(blocks []contentBlock) (string, []chat.Attachment, *rpcError) {
	var text []string
	var attachments []chat.Attachment
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text = append(text, b.Text)
		case "image":
			if b.Data == "" || !strings.HasPrefix(b.MIMEType, "image/") {
				return "", nil, &rpcError{Code: codeInvalidParams, Message: "图片必须提供 data 和 image mimeType"}
			}
			attachments = append(attachments, chat.Attachment{Kind: "image", MIMEType: b.MIMEType, URL: "data:" + b.MIMEType + ";base64," + b.Data})
		case "resource":
			if b.Resource == nil || b.Resource.Text == "" {
				return "", nil, &rpcError{Code: codeInvalidParams, Message: "嵌入资源必须含 text；本地路径请使用授权文件工具"}
			}
			text = append(text, b.Resource.Text)
		default:
			return "", nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("不支持内容类型 %q", b.Type)}
		}
	}
	joined := strings.Join(text, "\n")
	if strings.TrimSpace(joined) == "" && len(attachments) == 0 {
		return "", nil, &rpcError{Code: codeInvalidParams, Message: "提示内容为空"}
	}
	return joined, attachments, nil
}
