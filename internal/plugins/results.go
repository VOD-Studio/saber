package plugins

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/toolkit"
)

func imageData(data []byte, mime string) (string, error) {
	if len(data) == 0 || len(data) > 8<<20 {
		return "", errors.New("plugin image byte limit exceeded")
	}
	detected := http.DetectContentType(data)
	if detected != mime || !contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, mime) {
		return "", errors.New("plugin image MIME/content mismatch")
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
func safeImage(value string, external bool) (string, error) {
	if strings.HasPrefix(value, "data:") {
		parts := strings.SplitN(value, ",", 2)
		if len(parts) != 2 || !strings.HasSuffix(parts[0], ";base64") {
			return "", errors.New("plugin image data URL invalid")
		}
		data, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return "", errors.New("plugin image base64 invalid")
		}
		return imageData(data, strings.TrimSuffix(strings.TrimPrefix(parts[0], "data:"), ";base64"))
	}
	if !external {
		return "", errors.New("external image URL requires plugin external permission")
	}
	endpoint, err := url.Parse(value)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil {
		return "", errors.New("plugin image URL must be credential-free HTTPS")
	}
	return value, nil
}
func (m *Manager) richResult(ctx context.Context, result any, external bool) (any, error) {
	object, ok := result.(map[string]any)
	if !ok {
		return result, nil
	}
	if _, images := object["images"]; !images {
		if _, files := object["files"]; !files {
			return result, nil
		}
	}
	out := toolkit.Result{Data: object["data"]}
	if flag, ok := object["is_error"].(bool); ok {
		out.IsError = flag
	}
	if values, ok := object["images"].([]any); ok {
		for _, raw := range values {
			value, ok := raw.(string)
			if !ok {
				return nil, errors.New("plugin image must be URL/dataURL")
			}
			image, err := safeImage(value, external)
			if err != nil {
				return nil, err
			}
			out.Images = append(out.Images, image)
		}
	} else if _, exists := object["images"]; exists {
		return nil, errors.New("plugin images must be array")
	}
	if values, ok := object["files"].([]any); ok {
		if m.host.Workspace == nil {
			return nil, errors.New("plugin artifact workspace authorization unavailable")
		}
		workspace, err := m.host.Workspace(ctx)
		if err != nil {
			return nil, err
		}
		workspace, err = filepath.EvalSymlinks(workspace)
		if err != nil {
			return nil, err
		}
		for _, raw := range values {
			value, ok := raw.(string)
			if !ok {
				return nil, errors.New("plugin artifact path must be string")
			}
			if !filepath.IsAbs(value) {
				value = filepath.Join(workspace, value)
			}
			value, err = filepath.EvalSymlinks(value)
			if err != nil {
				return nil, err
			}
			relative, err := filepath.Rel(workspace, value)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, errors.New("plugin artifact escapes authorized workspace")
			}
			info, err := os.Stat(value)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, errors.New("plugin artifact must be regular file")
			}
			out.Files = append(out.Files, value)
		}
	} else if _, exists := object["files"]; exists {
		return nil, errors.New("plugin files must be array")
	}
	return out, nil
}
func mcpResult(result *mcp.CallToolResult) (toolkit.Result, error) {
	if result == nil {
		return toolkit.Result{}, errors.New("MCP connector returned no tool result")
	}
	out := toolkit.Result{IsError: result.IsError}
	text := []string{}
	for _, content := range result.Content {
		switch value := content.(type) {
		case *mcp.TextContent:
			text = append(text, value.Text)
		case *mcp.ImageContent:
			image, err := imageData(value.Data, value.MIMEType)
			if err != nil {
				return out, err
			}
			out.Images = append(out.Images, image)
		}
	}
	out.Data = map[string]any{"structured_content": result.StructuredContent, "text": strings.Join(text, "\n"), "result_type": resultTypeLabel(result), "input_requests": result.InputRequests, "request_state": result.RequestState}
	return out, nil
}

// resultTypeLabel 把 SDK 未导出的结果类型映射成稳定的协议取值，供工具结果回传。
func resultTypeLabel(result *mcp.CallToolResult) string {
	if result.NeedsInput() {
		return "input_required"
	}
	return "complete"
}
