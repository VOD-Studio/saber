package ai

import "rua.plus/saber/internal/chat"

type modelAttachment struct {
	Kind       string `json:"kind"`
	Name       string `json:"name,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	Size       int64  `json:"size,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

func attachmentMetadata(attachment chat.Attachment) modelAttachment {
	size := attachment.Size
	if size == 0 {
		size = int64(len(attachment.Data))
	}
	return modelAttachment{attachment.Kind, attachment.Name, attachment.MIMEType, size, attachment.DurationMS}
}

// modelMediaValue 仅裁剪真实 typed attachment，不按任意 JSON 键猜测载荷；没有附件时不复制数据。
func modelMediaValue(value any) (any, bool) {
	switch data := value.(type) {
	case chat.Attachment:
		return attachmentMetadata(data), true
	case *chat.Attachment:
		if data == nil {
			return nil, true
		}
		return attachmentMetadata(*data), true
	case []chat.Attachment:
		out := make([]modelAttachment, len(data))
		for index, attachment := range data {
			out[index] = attachmentMetadata(attachment)
		}
		return out, true
	case map[string]any:
		var out map[string]any
		for key, child := range data {
			replacement, changed := modelMediaValue(child)
			if !changed {
				continue
			}
			if out == nil {
				out = make(map[string]any, len(data))
				for k, v := range data {
					out[k] = v
				}
			}
			out[key] = replacement
		}
		if out != nil {
			return out, true
		}
	case []any:
		var out []any
		for index, child := range data {
			replacement, changed := modelMediaValue(child)
			if !changed {
				continue
			}
			if out == nil {
				out = append([]any(nil), data...)
			}
			out[index] = replacement
		}
		if out != nil {
			return out, true
		}
	}
	return value, false
}
