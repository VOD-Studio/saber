package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

type avatarStore struct {
	avatar chat.Avatar
	writes int
}

func (a *avatarStore) GetAvatar(context.Context) (chat.Avatar, error) { return a.avatar, nil }
func (a *avatarStore) SetAvatar(_ context.Context, data []byte, mimeType, _ string) (chat.Avatar, error) {
	if err := chat.ValidateAvatarImage(data, mimeType); err != nil {
		return chat.Avatar{}, err
	}
	a.avatar.URL = fmt.Sprintf("/avatars/%x", sha256.Sum256(data))
	a.writes++
	return a.avatar, nil
}

func avatarPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var out bytes.Buffer
	require.NoError(t, png.Encode(&out, img))
	return out.Bytes()
}

func avatarTestService(t *testing.T) (*Service, *config.Config, *avatarStore, context.Context, []byte) {
	t.Helper()
	cfg := emojiTestConfig()
	account := cfg.Platforms.Violet.EffectiveAccount()
	cfg.Commands.Admins = []config.CommandAdmin{{Platform: "violet", Account: account, Users: []string{"user-1"}}}
	service, err := NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(service.Stop)
	store := &avatarStore{avatar: chat.Avatar{URL: "/avatars/old"}}
	service.SetAvatarAdapter("violet", store)
	data := avatarPNG(t)
	ctx := withAvatarImages(emojiIdentityContext("violet", account), []openai.ChatCompletionMessage{{
		Role:         openai.ChatMessageRoleUser,
		MultiContent: []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}}},
	}})
	return service, cfg, store, ctx, data
}

func TestAvatarTool_RechecksIdentityAndRevocation(t *testing.T) {
	for _, scenario := range []string{"guest", "account", "platform", "disabled", "revoked", "missing_adapter", "public", "no_identity"} {
		t.Run(scenario, func(t *testing.T) {
			service, cfg, store, ctx, _ := avatarTestService(t)
			// 先取得工具快照，之后变更身份或配置，旧快照不能继续授权写操作。
			tools, _ := service.toolExecutor.PrepareTools(ctx)
			require.Contains(t, toolNames(tools), avatarToolName)
			switch scenario {
			case "guest":
				identity, _ := chat.IdentityFromContext(ctx)
				identity.SenderID = "guest"
				ctx = chat.WithIdentity(ctx, identity)
			case "account":
				ctx = emojiIdentityContext("violet", "another.example")
			case "platform":
				ctx = emojiIdentityContext("matrix", cfg.Platforms.Violet.EffectiveAccount())
			case "disabled":
				cfg.Platforms.Violet.Enabled = false
			case "revoked":
				cfg.Commands.Admins = nil
			case "missing_adapter":
				service.SetAvatarAdapter("violet", nil)
			case "public":
				ctx = withPublicThread(ctx)
			case "no_identity":
				ctx = context.Background()
			}
			_, err := service.toolExecutor.ExecuteToolCall(ctx, avatarToolName, map[string]any{"action": "set", "attachment": 1})
			require.Error(t, err)
			require.Equal(t, "/avatars/old", store.avatar.URL)
			require.Zero(t, store.writes)
		})
	}
}

func TestAvatarTool_RejectsInvalidImageSources(t *testing.T) {
	for _, args := range []map[string]any{
		{"action": "set"}, {"action": "set", "attachment": 0}, {"action": "set", "attachment": 1.5},
		{"action": "set", "attachment": 2}, {"action": "set", "attachment": 1, "image_path": "a.png"},
		{"action": "set", "image_path": "a.png"}, {"action": "invalid", "attachment": 1},
	} {
		service, _, store, ctx, _ := avatarTestService(t)
		_, err := service.toolExecutor.ExecuteToolCall(ctx, avatarToolName, args)
		require.Error(t, err, "args=%v", args)
		require.Equal(t, "/avatars/old", store.avatar.URL)
	}
	for _, source := range []string{"https://example.com/a.png", "data:image/png;base64,bad!", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("<html>error</html>"))} {
		service, _, store, ctx, _ := avatarTestService(t)
		ctx = context.WithValue(ctx, avatarImagesKey{}, []string{source})
		_, err := service.toolExecutor.ExecuteToolCall(ctx, avatarToolName, map[string]any{"action": "set", "attachment": 1})
		require.Error(t, err)
		require.Equal(t, "/avatars/old", store.avatar.URL)
	}
}

func TestAvatarImages_ExcludesHistoricalAttachments(t *testing.T) {
	service, _, store, ctx, data := avatarTestService(t)
	ctx = withAvatarImages(ctx, []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, MultiContent: []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}}}},
		{Role: openai.ChatMessageRoleUser, Content: "换头像"},
	})
	_, err := service.toolExecutor.ExecuteToolCall(ctx, avatarToolName, map[string]any{"action": "set", "attachment": 1})
	require.Error(t, err)
	require.Equal(t, "/avatars/old", store.avatar.URL)
}

func TestReadWorkspaceImage_RejectsEscapesAndNonImages(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	data := avatarPNG(t)
	require.NoError(t, os.WriteFile(outside, data, 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.png")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "error.png"), []byte("<html>error</html>"), 0600))
	for _, path := range []string{outside, "../outside.png", "escape.png", "error.png", "."} {
		_, _, _, err := readWorkspaceImage(root, path, maxAvatarImageBytes)
		require.Error(t, err, "path=%s", path)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "avatar.png"), data, 0600))
	got, mimeType, _, err := readWorkspaceImage(root, "avatar.png", maxAvatarImageBytes)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.Equal(t, "image/png", mimeType)
}
