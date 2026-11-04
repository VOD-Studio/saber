package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/conversation"
)

// testEmojiUUID 是测试用的自定义表情 ID。
const testEmojiUUID = "8180be12-9b13-4d3a-b18e-edf7a149a1e4"

// emojiTestConfig 构造启用 Violet 的测试配置；不打开推文接入，证明表情工具独立于推文。
func emojiTestConfig() *config.Config {
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "test-key"}}
	cfg.AI.DefaultModel = "openai.gpt-4"
	cfg.Platforms.Violet = config.DefaultVioletConfig()
	cfg.Platforms.Violet.Enabled = true
	cfg.Platforms.Violet.Endpoint = "https://blog.example.com"
	cfg.Platforms.Violet.BotToken = "violet_bot_test"
	return &cfg
}

// fakeEmojiAdapter 记录目录读取与收藏调用，供断言参数与结果。
type fakeEmojiAdapter struct {
	catalog       chat.EmojiCatalog
	listCalls     int
	listErr       error
	favoriteErr   error
	favoriteIDs   []string
	unfavoriteIDs []string
}

func (f *fakeEmojiAdapter) ListEmojis(context.Context) (chat.EmojiCatalog, error) {
	f.listCalls++
	return f.catalog, f.listErr
}

func (f *fakeEmojiAdapter) FavoriteEmoji(_ context.Context, emojiID string) error {
	f.favoriteIDs = append(f.favoriteIDs, emojiID)
	return f.favoriteErr
}

func (f *fakeEmojiAdapter) UnfavoriteEmoji(_ context.Context, emojiID string) error {
	f.unfavoriteIDs = append(f.unfavoriteIDs, emojiID)
	return f.favoriteErr
}

// chatEmojiCatalog 返回带系统分组与私有收藏的目录，token 是断言的核心。
func chatEmojiCatalog() chat.EmojiCatalog {
	return chat.EmojiCatalog{
		Groups: []chat.EmojiGroup{
			{ID: "g1", Name: "开心", Type: "image", Emojis: []chat.Emoji{
				{ID: "e1", Name: "doge", Token: "[doge]", GroupType: "image"},
				{ID: "e2", Name: "smile", Token: "[smile]", GroupType: "image"},
			}},
			{ID: "g2", Name: "动物", Type: "image", Emojis: []chat.Emoji{
				{ID: "e3", Name: "cat", Token: "[cat-system]", GroupType: "image"},
			}},
		},
		Custom: []chat.CustomEmoji{
			{ID: testEmojiUUID, Name: "节庆猫", Token: "[cat:" + testEmojiUUID + "]", Source: "favorited"},
		},
	}
}

// newEmojiTestService 构造注入了假表情端口的 AI 服务。
func newEmojiTestService(t *testing.T, cfg *config.Config) (*Service, *fakeEmojiAdapter) {
	t.Helper()
	service, err := NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(service.Stop)
	adapter := &fakeEmojiAdapter{catalog: chatEmojiCatalog()}
	service.SetEmojiAdapter(adapter)
	return service, adapter
}

// emojiIdentityContext 返回指定平台与账号下的可信身份上下文。
func emojiIdentityContext(platformName, account string) context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{
		Session:  chat.Session{Platform: platformName, Account: account, Conversation: "conv-1"},
		SenderID: "user-1",
	})
}

// TestPrepareTools_EmojiVisibility 验证 saber_emoji 只在启用的 Violet 账号下出现。
func TestPrepareTools_EmojiVisibility(t *testing.T) {
	cfg := emojiTestConfig()
	account := cfg.Platforms.Violet.EffectiveAccount()

	t.Run("启用的 violet 身份", func(t *testing.T) {
		service, _ := newEmojiTestService(t, cfg)
		tools, ok := service.toolExecutor.PrepareTools(emojiIdentityContext("violet", account))
		require.True(t, ok)
		require.Contains(t, toolNames(tools), emojiToolName)
	})

	t.Run("非 violet 平台", func(t *testing.T) {
		service, _ := newEmojiTestService(t, cfg)
		tools, ok := service.toolExecutor.PrepareTools(emojiIdentityContext("telegram", account))
		require.False(t, ok)
		require.NotContains(t, toolNames(tools), emojiToolName)
	})

	t.Run("账号不匹配", func(t *testing.T) {
		service, _ := newEmojiTestService(t, cfg)
		tools, ok := service.toolExecutor.PrepareTools(emojiIdentityContext("violet", "other.example.com"))
		require.False(t, ok)
		require.NotContains(t, toolNames(tools), emojiToolName)
	})

	t.Run("平台关闭", func(t *testing.T) {
		disabled := emojiTestConfig()
		disabled.Platforms.Violet.Enabled = false
		service, _ := newEmojiTestService(t, disabled)
		tools, ok := service.toolExecutor.PrepareTools(emojiIdentityContext("violet", account))
		require.False(t, ok)
		require.NotContains(t, toolNames(tools), emojiToolName)
	})

	t.Run("未注入端口", func(t *testing.T) {
		service, err := NewService(emojiTestConfig())
		require.NoError(t, err)
		t.Cleanup(service.Stop)
		tools, ok := service.toolExecutor.PrepareTools(emojiIdentityContext("violet", account))
		require.False(t, ok)
		require.NotContains(t, toolNames(tools), emojiToolName)
	})

	t.Run("公开回合无工具", func(t *testing.T) {
		service, _ := newEmojiTestService(t, cfg)
		tools, ok := service.toolExecutor.PrepareTools(withPublicThread(emojiIdentityContext("violet", account)))
		require.False(t, ok)
		require.Empty(t, tools)
	})
}

// TestExecuteEmojiTool_List 验证关键词、分组与条数过滤，以及无结果时的引导。
func TestExecuteEmojiTool_List(t *testing.T) {
	cfg := emojiTestConfig()
	service, adapter := newEmojiTestService(t, cfg)
	ctx := emojiIdentityContext("violet", cfg.Platforms.Violet.EffectiveAccount())

	list := func(args map[string]any) string {
		t.Helper()
		value, err := service.toolExecutor.ExecuteToolCall(ctx, emojiToolName, args)
		require.NoError(t, err)
		text, ok := value.(string)
		require.True(t, ok, "工具结果应该是文本")
		return text
	}

	out := list(map[string]any{"action": "list", "query": "DOGE"})
	require.Contains(t, out, "token=[doge]")
	require.NotContains(t, out, "token=[smile]")
	require.Contains(t, out, "不要臆造 token")

	out = list(map[string]any{"action": "list", "group": "动物"})
	require.Contains(t, out, "token=[cat-system]")
	require.NotContains(t, out, "token=[doge]")
	require.NotContains(t, out, "token=[cat:"+testEmojiUUID+"]")

	out = list(map[string]any{"action": "list", "query": "节庆"})
	require.Contains(t, out, "token=[cat:"+testEmojiUUID+"]")
	require.Contains(t, out, "source=favorited")

	out = list(map[string]any{"action": "list", "query": "开心"})
	require.Contains(t, out, "token=[doge]")
	require.Contains(t, out, "token=[smile]")

	out = list(map[string]any{"action": "list", "limit": 2})
	require.Equal(t, 2, strings.Count(out, "token="), "limit 必须限制展示条数")

	out = list(map[string]any{"action": "list", "query": "不存在"})
	require.Contains(t, out, "没有找到")
	require.Contains(t, out, "换一个关键词")
	require.Equal(t, 6, adapter.listCalls)
}

// TestExecuteEmojiTool_Favorite 验证按 UUID 与 token 收藏，以及系统 token 被拒绝。
func TestExecuteEmojiTool_Favorite(t *testing.T) {
	cfg := emojiTestConfig()
	service, adapter := newEmojiTestService(t, cfg)
	ctx := emojiIdentityContext("violet", cfg.Platforms.Violet.EffectiveAccount())

	value, err := service.toolExecutor.ExecuteToolCall(ctx, emojiToolName, map[string]any{
		"action": "favorite", "token": "[cat:" + testEmojiUUID + "]",
	})
	require.NoError(t, err)
	require.Contains(t, value.(string), "已收藏")
	require.Equal(t, []string{testEmojiUUID}, adapter.favoriteIDs)

	_, err = service.toolExecutor.ExecuteToolCall(ctx, emojiToolName, map[string]any{
		"action": "favorite", "emoji_id": testEmojiUUID,
	})
	require.NoError(t, err)
	require.Equal(t, []string{testEmojiUUID, testEmojiUUID}, adapter.favoriteIDs)

	_, err = service.toolExecutor.ExecuteToolCall(ctx, emojiToolName, map[string]any{
		"action": "unfavorite", "token": "[cat:" + testEmojiUUID + "]",
	})
	require.NoError(t, err)
	require.Equal(t, []string{testEmojiUUID}, adapter.unfavoriteIDs)

	_, err = service.toolExecutor.ExecuteToolCall(ctx, emojiToolName, map[string]any{
		"action": "favorite", "token": "[doge]",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "系统表情")

	_, err = service.toolExecutor.ExecuteToolCall(ctx, emojiToolName, map[string]any{
		"action": "favorite", "token": "[cat:not-a-uuid]",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "UUID")

	_, err = service.toolExecutor.ExecuteToolCall(ctx, emojiToolName, map[string]any{
		"action": "favorite", "emoji_id": "cat",
	})
	require.Error(t, err)

	_, err = service.toolExecutor.ExecuteToolCall(ctx, emojiToolName, map[string]any{
		"action": "favorite",
	})
	require.Error(t, err)
	require.Len(t, adapter.favoriteIDs, 2, "失败的目标解析不能落成收藏调用")
}

// TestExecuteEmojiTool_RequiresVioletIdentity 验证执行阶段复核平台、账号与公开回合。
func TestExecuteEmojiTool_RequiresVioletIdentity(t *testing.T) {
	cfg := emojiTestConfig()
	service, adapter := newEmojiTestService(t, cfg)
	args := map[string]any{"action": "list"}

	_, err := service.toolExecutor.ExecuteToolCall(emojiIdentityContext("telegram", cfg.Platforms.Violet.EffectiveAccount()), emojiToolName, args)
	require.Error(t, err)

	_, err = service.toolExecutor.ExecuteToolCall(emojiIdentityContext("violet", "other.example.com"), emojiToolName, args)
	require.Error(t, err)

	_, err = service.toolExecutor.ExecuteToolCall(withPublicThread(emojiIdentityContext("violet", cfg.Platforms.Violet.EffectiveAccount())), emojiToolName, args)
	require.Error(t, err)
	require.Contains(t, err.Error(), "公开互动回合")

	_, err = service.toolExecutor.ExecuteToolCall(emojiIdentityContext("violet", cfg.Platforms.Violet.EffectiveAccount()), emojiToolName, map[string]any{"action": "bogus"})
	require.Error(t, err)
	require.Zero(t, adapter.listCalls)
}

// TestEmojiTool_ScheduleChatOnly 验证 saber_emoji 属于纯聊天工具，可被计划冻结而不申请执行授权。
func TestEmojiTool_ScheduleChatOnly(t *testing.T) {
	t.Parallel()
	tool := emojiTool()
	require.Equal(t, emojiToolName, tool.Function.Name)
	require.False(t, scheduleNeedsExecution([]openai.Tool{tool}))
	require.Equal(t, []string{emojiToolName}, toolNames(filterChatTools([]openai.Tool{tool})))
	require.True(t, builtinToolName(emojiToolName))

	params, ok := tool.Function.Parameters.(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, params["additionalProperties"])
	require.Contains(t, tool.Function.Description, "token 原样写进回复正文")
}

// TestHandleChat_EmojiInstruction 验证普通聊天注入表情说明，公开推文回合不注入。
func TestHandleChat_EmojiInstruction(t *testing.T) {
	cfg := emojiTestConfig()
	service, _ := newEmojiTestService(t, cfg)
	account := cfg.Platforms.Violet.EffectiveAccount()

	var captured agent.Request
	service.chatProcessor = &conversation.Processor{
		Run: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
			captured = req
			return agent.Result{Status: agent.Completed, Content: "好的"}, nil
		},
		Timeout: time.Minute,
	}

	message := chat.Message{
		Session:  chat.Session{Platform: "violet", Account: account, Conversation: "conv-1"},
		ID:       "msg-1",
		SenderID: "user-1",
		Text:     "今天心情不错",
	}
	_, err := service.HandleChat(context.Background(), message, &countingReplyAdapter{})
	require.NoError(t, err)
	require.True(t, hasSystemInstruction(captured, emojiReplyInstruction), "普通 Violet 聊天应注入表情说明")
	require.Contains(t, toolNames(captured.Tools), emojiToolName)

	captured = agent.Request{}
	_, err = service.HandleTweet(context.Background(), message, &countingReplyAdapter{})
	require.NoError(t, err)
	require.False(t, hasSystemInstruction(captured, emojiReplyInstruction), "公开推文回合不能注入表情说明")
	require.Empty(t, captured.Tools)

	captured = agent.Request{}
	message.Session.Account = "other.example.com"
	_, err = service.HandleChat(context.Background(), message, &countingReplyAdapter{})
	require.NoError(t, err)
	require.False(t, hasSystemInstruction(captured, emojiReplyInstruction), "非本账号身份不该注入表情说明")
}

// hasSystemInstruction 判断请求里是否包含指定系统消息。
func hasSystemInstruction(req agent.Request, content string) bool {
	for _, message := range req.Messages {
		if message.Role == openai.ChatMessageRoleSystem && message.Content == content {
			return true
		}
	}
	return false
}
