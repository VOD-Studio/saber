package ai

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

const avatarToolName = "saber_avatar"
const maxAvatarImageBytes = 10 << 20

type avatarImagesKey struct{}

// withAvatarImages 保留本轮用户附件；历史图片与模型编造的 URL 不能成为上传来源。
func withAvatarImages(ctx context.Context, messages []openai.ChatCompletionMessage) context.Context {
	var images []string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != openai.ChatMessageRoleUser {
			continue
		}
		for _, part := range messages[i].MultiContent {
			if part.Type == openai.ChatMessagePartTypeImageURL && part.ImageURL != nil {
				images = append(images, part.ImageURL.URL)
			}
		}
		break
	}
	return context.WithValue(ctx, avatarImagesKey{}, images)
}

func avatarTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: avatarToolName,
		Description: "读取或更换 Saber 在当前聊天平台（Matrix/Violet）的账号头像，不会修改房间头像、其他用户或其他平台。" +
			"get 查询当前公开头像；set 会公开更换头像，仅 commands.admins 授权用户可执行。" +
			"set 使用本轮用户图片附件的 attachment（从 1 开始），或已授权任务工作区内的 image_path 相对路径，二选一。" +
			"支持 PNG/JPEG/GIF/WebP，最多 10 MiB。没有图片时先请用户提供或在已授权工作区准备图片，不要臆造 URL；仅工具成功后才能声称已更换。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":     map[string]any{"type": "string", "enum": []string{"get", "set"}},
				"attachment": map[string]any{"type": "integer", "minimum": 1, "description": "set：本轮用户图片附件编号，从 1 开始"},
				"image_path": map[string]any{"type": "string", "description": "set：已授权任务工作区内的图片相对路径，与 attachment 二选一"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}}
}

// SetAvatarAdapter 按平台登记自身头像端口；可见性与执行时仍复核当前配置和账号。
func (s *Service) SetAvatarAdapter(platformName string, adapter chat.AvatarAdapter) {
	if adapter == nil {
		s.avatarAdapters.Delete(platformName)
		return
	}
	s.avatarAdapters.Store(platformName, adapter)
}

func (s *Service) avatarAdapter(ctx context.Context) chat.AvatarAdapter {
	if s == nil || s.config == nil || isPublicThread(ctx) {
		return nil
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil
	}
	switch identity.Session.Platform {
	case "matrix":
		if !s.config.Platforms.Matrix.Enabled || identity.Session.Account != s.config.Matrix.UserID {
			return nil
		}
	case "violet":
		if !s.config.Platforms.Violet.Enabled || identity.Session.Account != s.config.Platforms.Violet.EffectiveAccount() {
			return nil
		}
	default:
		return nil
	}
	adapter, ok := s.avatarAdapters.Load(identity.Session.Platform)
	if !ok {
		return nil
	}
	return adapter.(chat.AvatarAdapter)
}

func (s *Service) executeAvatarTool(ctx context.Context, args map[string]any) (any, error) {
	adapter := s.avatarAdapter(ctx)
	if adapter == nil {
		return nil, errors.New("头像工具不可用：需要已启用且账号匹配的 Matrix 或 Violet 可信身份")
	}
	switch stringArg(args, "action") {
	case "get":
		return adapter.GetAvatar(ctx)
	case "set":
		identity, _ := chat.IdentityFromContext(ctx)
		if !s.config.Commands.IsAdmin(identity) {
			return nil, errors.New("更换公开账号头像需要 commands.admins 授权")
		}
		data, mimeType, filename, err := s.avatarImage(ctx, args)
		if err != nil {
			return nil, err
		}
		avatar, err := adapter.SetAvatar(ctx, data, mimeType, filename)
		if err != nil {
			return nil, fmt.Errorf("更换头像失败: %w", err)
		}
		return avatar, nil
	default:
		return nil, errors.New("头像 action 必须为 get 或 set")
	}
}

func (s *Service) avatarImage(ctx context.Context, args map[string]any) ([]byte, string, string, error) {
	path := stringArg(args, "image_path")
	attachment, hasAttachment := args["attachment"]
	if (path != "") == hasAttachment {
		return nil, "", "", errors.New("set 必须提供 attachment 或 image_path，且不能同时提供")
	}
	if path != "" {
		workDir := strings.TrimSpace(task.WorkDir(ctx))
		if workDir == "" || s.executor == nil {
			return nil, "", "", errors.New("image_path 需要已授权的任务工作目录")
		}
		if err := s.executor.Check(ctx, "read_file"); err != nil {
			return nil, "", "", fmt.Errorf("读取头像图片未获授权: %w", err)
		}
		return readWorkspaceImage(workDir, path, maxAvatarImageBytes)
	}
	number, err := strconv.Atoi(fmt.Sprint(attachment))
	if err != nil || number <= 0 {
		return nil, "", "", errors.New("attachment 必须是从 1 开始的整数")
	}
	images, _ := ctx.Value(avatarImagesKey{}).([]string)
	if number > len(images) {
		return nil, "", "", fmt.Errorf("本轮只有 %d 张图片附件，无法读取 attachment=%d", len(images), number)
	}
	header, encoded, ok := strings.Cut(images[number-1], ",")
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
		return nil, "", "", errors.New("头像附件必须是平台已下载的图片，不接受远程 URL")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxAvatarImageBytes) {
		return nil, "", "", errors.New("头像图片超过 10 MiB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", "", fmt.Errorf("解析头像附件失败: %w", err)
	}
	mimeType, err := platformImageMIME(data, maxAvatarImageBytes)
	if err != nil {
		return nil, "", "", err
	}
	return data, mimeType, "avatar." + strings.TrimPrefix(strings.ReplaceAll(mimeType, "jpeg", "jpg"), "image/"), nil
}

func platformImageMIME(data []byte, limit int) (string, error) {
	if len(data) == 0 || len(data) > limit {
		return "", fmt.Errorf("图片大小必须为 1..%d 字节", limit)
	}
	mimeType := http.DetectContentType(data)
	if err := chat.ValidateAvatarImage(data, mimeType); err != nil {
		return "", err
	}
	return mimeType, nil
}
