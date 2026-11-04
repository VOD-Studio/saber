package ai

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/conversation"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/memory"
)

// tweetTestConfig 构造启用推文接入的测试配置：默认账号取 endpoint 主机名。
func tweetTestConfig() *config.Config {
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "test-key"}}
	cfg.AI.DefaultModel = "openai.gpt-4"
	cfg.Platforms.Violet = config.DefaultVioletConfig()
	cfg.Platforms.Violet.Enabled = true
	cfg.Platforms.Violet.Endpoint = "https://blog.example.com"
	cfg.Platforms.Violet.BotToken = "violet_bot_test"
	cfg.Platforms.Violet.Tweets.Enabled = true
	cfg.Platforms.Violet.Tweets.MaxPostsPerDay = 8
	cfg.Platforms.Violet.Tweets.MinPostIntervalMinutes = 30
	cfg.Commands.Admins = []config.CommandAdmin{{Platform: "violet", Account: "blog.example.com", Users: []string{"admin-1"}}}
	return &cfg
}

// fakeTweetAdapter 记录每一次推文读写调用，供断言参数、顺序与幂等键。
type fakeTweetAdapter struct {
	postDrafts      []chat.TweetDraft
	postKeys        []string
	commentTweetIDs []string
	commentDrafts   []chat.TweetCommentDraft
	commentKeys     []string
	likeIDs         []string
	likeStates      []bool
	deletedTweets   []string
	deletedComments [][2]string
	uploadedData    [][]byte
	uploadedMIME    []string
	uploadedNames   []string

	getTweet         chat.Tweet
	getErr           error
	comments         []chat.TweetComment
	commentLimits    []int
	commentParents   []string
	listErr          error
	createErr        error
	createCommentErr error
	uploadErr        error
}

func (f *fakeTweetAdapter) CreateTweet(_ context.Context, draft chat.TweetDraft, key string) (chat.Tweet, error) {
	f.postDrafts = append(f.postDrafts, draft)
	f.postKeys = append(f.postKeys, key)
	if f.createErr != nil {
		return chat.Tweet{}, f.createErr
	}
	return chat.Tweet{ID: "tweet-1", Author: chat.TweetAuthor{Username: "saberbot", IsBot: true}, Content: draft.Content}, nil
}

func (f *fakeTweetAdapter) CreateComment(_ context.Context, tweetID string, draft chat.TweetCommentDraft, key string) (chat.TweetComment, error) {
	f.commentTweetIDs = append(f.commentTweetIDs, tweetID)
	f.commentDrafts = append(f.commentDrafts, draft)
	f.commentKeys = append(f.commentKeys, key)
	if f.createCommentErr != nil {
		return chat.TweetComment{}, f.createCommentErr
	}
	return chat.TweetComment{ID: "comment-1", TweetID: tweetID, Body: draft.Body}, nil
}

func (f *fakeTweetAdapter) LikeTweet(_ context.Context, tweetID string, on bool) error {
	f.likeIDs = append(f.likeIDs, tweetID)
	f.likeStates = append(f.likeStates, on)
	return nil
}

func (f *fakeTweetAdapter) DeleteTweet(_ context.Context, tweetID string) error {
	f.deletedTweets = append(f.deletedTweets, tweetID)
	return nil
}

func (f *fakeTweetAdapter) DeleteComment(_ context.Context, tweetID, commentID string) error {
	f.deletedComments = append(f.deletedComments, [2]string{tweetID, commentID})
	return nil
}

func (f *fakeTweetAdapter) UploadImage(_ context.Context, data []byte, mimeType, filename string) (chat.TweetMedia, error) {
	f.uploadedData = append(f.uploadedData, data)
	f.uploadedMIME = append(f.uploadedMIME, mimeType)
	f.uploadedNames = append(f.uploadedNames, filename)
	if f.uploadErr != nil {
		return chat.TweetMedia{}, f.uploadErr
	}
	return chat.TweetMedia{ID: "media-1", URL: "https://cdn.example.com/media-1.png", MIMEType: mimeType, Width: 10, Height: 20, Size: int64(len(data))}, nil
}

func (f *fakeTweetAdapter) GetTweet(context.Context, string) (chat.Tweet, error) {
	return f.getTweet, f.getErr
}

func (f *fakeTweetAdapter) ListComments(_ context.Context, _, parentCommentID string, limit int) ([]chat.TweetComment, error) {
	f.commentLimits = append(f.commentLimits, limit)
	f.commentParents = append(f.commentParents, parentCommentID)
	return f.comments, f.listErr
}

func (f *fakeTweetAdapter) BotUsername() string { return "saberbot" }

// countingReplyAdapter 只统计最终发送次数，用于确认推文评论只发一条。
type countingReplyAdapter struct {
	sends int
	last  chat.Reply
}

func (a *countingReplyAdapter) Capabilities() chat.Capabilities { return chat.Capabilities{} }

func (a *countingReplyAdapter) Send(_ context.Context, reply chat.Reply) (string, error) {
	a.sends++
	a.last = reply
	return "comment-1", nil
}

func (a *countingReplyAdapter) Edit(context.Context, string, chat.Reply) error { return nil }

func (a *countingReplyAdapter) SetTyping(context.Context, chat.Session, bool) error { return nil }

// newTweetTestService 构造启用推文工具的 AI 服务。
func newTweetTestService(t *testing.T, cfg *config.Config, opts ...ServiceOption) (*Service, *fakeTweetAdapter) {
	t.Helper()
	service, err := NewService(cfg, opts...)
	require.NoError(t, err)
	adapter := &fakeTweetAdapter{getTweet: chat.Tweet{
		ID:           "tweet-9",
		Author:       chat.TweetAuthor{Username: "alice", DisplayName: "Alice", IsBot: false},
		Content:      "今天写完了新文章",
		Images:       []chat.TweetImage{{URL: "https://cdn.example.com/a.png", Width: 100, Height: 50}},
		LikeCount:    3,
		CommentCount: 2,
		QuoteCount:   1,
		IsLiked:      true,
		CreatedAt:    time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC),
	}}
	service.SetTweetAdapter(adapter)
	return service, adapter
}

// tweetAdminContext 返回具备管理员授权的 Violet 身份上下文。
func tweetAdminContext(account string) context.Context {
	identity := chat.Identity{
		Session:  chat.Session{Platform: "violet", Account: account, Conversation: "tweet:tweet-9"},
		SenderID: "admin-1",
	}
	return chat.WithIdentity(context.Background(), identity)
}

// grantReadFile 构造只授权 read_file 的执行器，用来证明公开回合确实没有任何工具。
func grantReadFile(t *testing.T, account string) (*execution.Executor, string) {
	t.Helper()
	workdir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	cfg := config.ExecutionConfig{
		Enabled:    true,
		Backend:    "local",
		LogDir:     t.TempDir(),
		Workspaces: map[string]config.WorkspaceConfig{"project": {Path: workdir}},
		Grants: []config.ExecutionGrant{{
			Platform: "violet", Account: account, Room: "tweet:tweet-9",
			Users: []string{"admin-1"}, Workspace: "project", Tools: []string{"read_file"},
		}},
	}
	exec, err := execution.New(cfg, nil, nil)
	require.NoError(t, err)
	return exec, workdir
}

// TestPrepareTools_TweetVisibility 验证 saber_tweet 只在启用的 Violet 身份下出现。
func TestPrepareTools_TweetVisibility(t *testing.T) {
	t.Run("enabled violet identity", func(t *testing.T) {
		cfg := tweetTestConfig()
		service, _ := newTweetTestService(t, cfg)
		ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

		tools, ok := service.toolExecutor.PrepareTools(ctx)
		require.True(t, ok)
		require.Contains(t, toolNames(tools), tweetToolName)
		require.True(t, builtinToolName(tweetToolName))
	})

	t.Run("other platform identity", func(t *testing.T) {
		cfg := tweetTestConfig()
		service, _ := newTweetTestService(t, cfg)
		ctx := chat.WithIdentity(context.Background(), chat.Identity{
			Session:  chat.Session{Platform: "telegram", Account: cfg.Platforms.Violet.EffectiveAccount(), Conversation: "chat"},
			SenderID: "admin-1",
		})

		tools, _ := service.toolExecutor.PrepareTools(ctx)
		require.NotContains(t, toolNames(tools), tweetToolName)
	})

	t.Run("no trusted identity", func(t *testing.T) {
		cfg := tweetTestConfig()
		service, _ := newTweetTestService(t, cfg)

		tools, _ := service.toolExecutor.PrepareTools(context.Background())
		require.NotContains(t, toolNames(tools), tweetToolName)
	})

	t.Run("violet platform disabled", func(t *testing.T) {
		cfg := tweetTestConfig()
		cfg.Platforms.Violet.Enabled = false
		service, _ := newTweetTestService(t, cfg)
		ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

		tools, _ := service.toolExecutor.PrepareTools(ctx)
		require.NotContains(t, toolNames(tools), tweetToolName)
	})

	t.Run("tweets disabled", func(t *testing.T) {
		cfg := tweetTestConfig()
		cfg.Platforms.Violet.Tweets.Enabled = false
		service, _ := newTweetTestService(t, cfg)
		ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

		tools, _ := service.toolExecutor.PrepareTools(ctx)
		require.NotContains(t, toolNames(tools), tweetToolName)
	})

	t.Run("account mismatch", func(t *testing.T) {
		cfg := tweetTestConfig()
		service, _ := newTweetTestService(t, cfg)
		ctx := tweetAdminContext("other.example.com")

		tools, _ := service.toolExecutor.PrepareTools(ctx)
		require.NotContains(t, toolNames(tools), tweetToolName)
	})

	t.Run("adapter not injected", func(t *testing.T) {
		cfg := tweetTestConfig()
		service, _ := newTweetTestService(t, cfg)
		service.SetTweetAdapter(nil)
		ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

		tools, _ := service.toolExecutor.PrepareTools(ctx)
		require.NotContains(t, toolNames(tools), tweetToolName)
	})

	t.Run("public thread exposes nothing", func(t *testing.T) {
		cfg := tweetTestConfig()
		account := cfg.Platforms.Violet.EffectiveAccount()
		service, _ := newTweetTestService(t, cfg)
		exec, workDir := grantReadFile(t, account)
		service.executor = exec
		ctx := execution.WithTask(tweetAdminContext(account), 1, workDir)

		tools, ok := service.toolExecutor.PrepareTools(ctx)
		require.True(t, ok)
		require.Contains(t, toolNames(tools), "read_file")
		require.Contains(t, toolNames(tools), tweetToolName)

		tools, ok = service.toolExecutor.PrepareTools(withPublicThread(ctx))
		require.False(t, ok)
		require.Empty(t, tools)
	})
}

// TestExecuteTweetTool_WriteRequiresAdmin 验证写操作在执行阶段复核 commands.admins。
func TestExecuteTweetTool_WriteRequiresAdmin(t *testing.T) {
	cfg := tweetTestConfig()
	service, adapter := newTweetTestService(t, cfg)
	guest := chat.WithIdentity(context.Background(), chat.Identity{
		Session:  chat.Session{Platform: "violet", Account: cfg.Platforms.Violet.EffectiveAccount(), Conversation: "tweet:tweet-9"},
		SenderID: "guest-1",
	})

	for _, args := range []map[string]any{
		{"action": "post", "content": "hi"},
		{"action": "reply", "tweet_id": "tweet-9", "body": "hi"},
		{"action": "like", "tweet_id": "tweet-9"},
		{"action": "delete", "tweet_id": "tweet-9"},
	} {
		_, err := service.toolExecutor.ExecuteToolCall(guest, tweetToolName, args)
		require.Error(t, err, "非管理员执行 %v 必须被拒绝", args["action"])
	}
	require.Empty(t, adapter.postDrafts)
	require.Empty(t, adapter.commentDrafts)
	require.Empty(t, adapter.likeIDs)
	require.Empty(t, adapter.deletedTweets)
}

// TestExecuteTweetTool_Writes 覆盖管理员发布、回复、点赞与删除的成功路径。
func TestExecuteTweetTool_Writes(t *testing.T) {
	cfg := tweetTestConfig()
	service, adapter := newTweetTestService(t, cfg)
	ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

	value, err := service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{
		"action": "post", "content": "  新推文  ", "quote_of": "tweet-8",
	})
	require.NoError(t, err)
	require.Contains(t, fmt.Sprint(value), "tweet-1")
	require.Len(t, adapter.postDrafts, 1)
	require.Equal(t, "新推文", adapter.postDrafts[0].Content)
	require.Equal(t, "tweet-8", adapter.postDrafts[0].QuoteOf)
	require.NoError(t, uuid.Validate(adapter.postKeys[0]))

	value, err = service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{
		"action": "reply", "tweet_id": "tweet-9", "body": " 收到 ", "parent_comment_id": "comment-0",
	})
	require.NoError(t, err)
	require.Contains(t, fmt.Sprint(value), "comment-1")
	require.Len(t, adapter.commentDrafts, 1)
	require.Equal(t, "收到", adapter.commentDrafts[0].Body)
	require.Equal(t, "comment-0", adapter.commentDrafts[0].ParentID)
	require.Equal(t, "tweet-9", adapter.commentTweetIDs[0])
	require.NoError(t, uuid.Validate(adapter.commentKeys[0]))

	_, err = service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "like", "tweet_id": "tweet-9"})
	require.NoError(t, err)
	_, err = service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "unlike", "tweet_id": "tweet-9"})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, adapter.likeStates)

	_, err = service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "delete", "tweet_id": "tweet-9"})
	require.NoError(t, err)
	require.Equal(t, []string{"tweet-9"}, adapter.deletedTweets)

	_, err = service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "delete_comment", "tweet_id": "tweet-9", "comment_id": "comment-1"})
	require.NoError(t, err)
	require.Equal(t, [][2]string{{"tweet-9", "comment-1"}}, adapter.deletedComments)
}

// TestExecuteTweetTool_Reads 覆盖推文快照与评论列表的只读路径及条数限制。
func TestExecuteTweetTool_Reads(t *testing.T) {
	cfg := tweetTestConfig()
	service, adapter := newTweetTestService(t, cfg)
	ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())
	adapter.comments = []chat.TweetComment{
		{ID: "c-1", Author: chat.TweetAuthor{Username: "alice"}, Body: "第一条"},
		{ID: "c-2", ParentID: "c-1", Author: chat.TweetAuthor{Username: "bob"}},
	}

	value, err := service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "get", "tweet_id": "tweet-9"})
	require.NoError(t, err)
	snapshot := fmt.Sprint(value)
	require.Contains(t, snapshot, "tweet-9")
	require.Contains(t, snapshot, "@alice")
	require.Contains(t, snapshot, "今天写完了新文章")
	require.Contains(t, snapshot, "点赞 3")
	require.Contains(t, snapshot, "评论 2")
	require.Contains(t, snapshot, "2026-11-03T09:00:00Z")
	require.Contains(t, snapshot, "https://cdn.example.com/a.png")

	value, err = service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "comments", "tweet_id": "tweet-9"})
	require.NoError(t, err)
	list := fmt.Sprint(value)
	require.Contains(t, list, "c-1")
	require.Contains(t, list, "父 c-1")
	require.Contains(t, list, "@bob")
	require.Contains(t, list, "（纯图片评论）")
	require.Equal(t, []int{tweetCommentsDefaultLimit}, adapter.commentLimits)

	_, err = service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "comments", "tweet_id": "tweet-9", "limit": 999})
	require.NoError(t, err)
	require.Equal(t, []int{tweetCommentsDefaultLimit, tweetCommentsMaxLimit}, adapter.commentLimits)
}

// TestExecuteTweetTool_TruncatesContent 验证正文按 Unicode 字符裁剪。
func TestExecuteTweetTool_TruncatesContent(t *testing.T) {
	cfg := tweetTestConfig()
	cfg.Platforms.Violet.Tweets.MaxContentRunes = 5
	service, adapter := newTweetTestService(t, cfg)
	ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

	_, err := service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "post", "content": "一二三四五六七八"})
	require.NoError(t, err)
	require.Equal(t, "一二三四五", adapter.postDrafts[0].Content)
}

// TestExecuteTweetTool_PostLimiter 验证每日上限与最小间隔，失败时不得触达平台。
func TestExecuteTweetTool_PostLimiter(t *testing.T) {
	t.Run("daily limit", func(t *testing.T) {
		cfg := tweetTestConfig()
		cfg.Platforms.Violet.Tweets.MaxPostsPerDay = 2
		service, adapter := newTweetTestService(t, cfg)
		ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

		for i := range 2 {
			_, err := service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "post", "content": fmt.Sprintf("第 %d 条", i)})
			require.NoError(t, err)
			// 手动回拨上次时间，隔离每日上限与最小间隔两条规则。
			service.tweetPosts.mu.Lock()
			service.tweetPosts.last = time.Now().Add(-24 * time.Hour)
			service.tweetPosts.mu.Unlock()
		}
		_, err := service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "post", "content": "第三条"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "上限")
		require.Len(t, adapter.postDrafts, 2)
	})

	t.Run("min interval", func(t *testing.T) {
		cfg := tweetTestConfig()
		cfg.Platforms.Violet.Tweets.MaxPostsPerDay = 10
		cfg.Platforms.Violet.Tweets.MinPostIntervalMinutes = 10
		service, adapter := newTweetTestService(t, cfg)
		ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

		_, err := service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "post", "content": "第一条"})
		require.NoError(t, err)
		_, err = service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{"action": "post", "content": "第二条"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "最小间隔")
		require.Len(t, adapter.postDrafts, 1)
	})
}

// TestTweetKeyForTask 验证幂等键：同任务同参数稳定，跨任务或跨参数不同。
func TestTweetKeyForTask(t *testing.T) {
	payload := tweetPostKey{Content: "同一条内容"}
	first, err := tweetKey(42, "post", payload)
	require.NoError(t, err)
	second, err := tweetKey(42, "post", payload)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.NoError(t, uuid.Validate(first))

	otherTask, err := tweetKey(43, "post", payload)
	require.NoError(t, err)
	require.NotEqual(t, first, otherTask)

	otherPayload, err := tweetKey(42, "post", tweetPostKey{Content: "另一条内容"})
	require.NoError(t, err)
	require.NotEqual(t, first, otherPayload)

	randomOne, err := tweetKey(0, "post", payload)
	require.NoError(t, err)
	randomTwo, err := tweetKey(0, "post", payload)
	require.NoError(t, err)
	require.NotEqual(t, randomOne, randomTwo)
	require.NoError(t, uuid.Validate(randomOne))
	require.NoError(t, uuid.Validate(randomTwo))
}

// TestUploadTweetImage 验证工作区路径限制、大小与 MIME 校验。
func TestUploadTweetImage(t *testing.T) {
	cfg := tweetTestConfig()
	service, adapter := newTweetTestService(t, cfg)
	ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())
	workDir := t.TempDir()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "ok.png"), png, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "bad.txt"), []byte("not an image"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "large.png"), append(png, make([]byte, maxTweetImageBytes)...), 0o600))
	outside := filepath.Join(t.TempDir(), "outside.png")
	require.NoError(t, os.WriteFile(outside, png, 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(workDir, "link.png")))

	media, err := service.uploadTweetImage(ctx, workDir, "ok.png")
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.com/media-1.png", media.URL)
	require.Equal(t, []string{"image/png"}, adapter.uploadedMIME)
	require.Equal(t, []string{"ok.png"}, adapter.uploadedNames)

	for name, rel := range map[string]string{
		"escape":     "../outside.png",
		"absolute":   outside,
		"symlink":    "link.png",
		"too large":  "large.png",
		"wrong type": "bad.txt",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.uploadTweetImage(ctx, workDir, rel)
			require.Error(t, err)
		})
	}
	require.Len(t, adapter.uploadedData, 1, "失败的图片不能上传")
}

// TestTweetImages_Limits 验证图片张数上限与 0 = 禁止配图、以及缺少工作目录时拒绝上传。
func TestTweetImages_Limits(t *testing.T) {
	cfg := tweetTestConfig()
	cfg.Platforms.Violet.Tweets.MaxImagesPerTweet = 2
	service, adapter := newTweetTestService(t, cfg)
	ctx := tweetAdminContext(cfg.Platforms.Violet.EffectiveAccount())

	_, err := service.toolExecutor.ExecuteToolCall(ctx, tweetToolName, map[string]any{
		"action": "reply", "tweet_id": "tweet-9", "images": []string{"a.png", "b.png", "c.png"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "最多 2 张")
	require.Empty(t, adapter.uploadedData)

	_, err = service.tweetImages(ctx, []string{"a.png"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "任务工作目录")

	cfg.Platforms.Violet.Tweets.MaxImagesPerTweet = 0
	_, err = service.tweetImages(ctx, []string{"a.png"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "不允许推文配图")
	require.Empty(t, adapter.uploadedData)
}

// TestHandleTweet_PublicThreadTurn 验证推文回合一：不建任务、无工具、无记忆、
// 注入公开回复约束与不可信上下文，且只向回复 adapter 发送一次最终正文。
func TestHandleTweet_PublicThreadTurn(t *testing.T) {
	cfg := tweetTestConfig()
	mem := newTestMemory(t, memory.Config{GroupWriter: func(chat.Identity) bool { return true }})
	service, _ := newTweetTestService(t, cfg, WithMemory(mem))
	t.Cleanup(service.Stop)

	session := chat.Session{Platform: "violet", Account: cfg.Platforms.Violet.EffectiveAccount(), Conversation: "tweet:tweet-9"}
	identity := chat.Identity{Session: session, SenderID: "guest-1"}
	_, err := mem.Add(chat.WithIdentity(context.Background(), identity), identity, "私密记忆条目不应外泄", memory.Source{}, true)
	require.NoError(t, err)

	require.NoError(t, service.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	var captured agent.Request
	service.chatProcessor = &conversation.Processor{
		Run: func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
			captured = req
			return agent.Result{Status: agent.Completed, Content: "感谢你的评论"}, nil
		},
		Timeout: time.Minute,
	}
	reply := &countingReplyAdapter{}
	message := chat.Message{
		Session:  session,
		ID:       "comment-1",
		SenderID: "guest-1",
		Text:     "你怎么看这篇文章？",
		Context:  "[Violet 推文互动] 推文正文: hello",
	}

	result, err := service.HandleTweet(context.Background(), message, reply)
	require.NoError(t, err)
	require.Equal(t, "感谢你的评论", result.Content)
	require.Empty(t, captured.Tools, "公开回合不能提供任何工具")
	require.Equal(t, 1, reply.sends, "最终评论只发送一次")
	require.Equal(t, "感谢你的评论", reply.last.Text)

	tasks, err := service.tasks.List(context.Background(), session)
	require.NoError(t, err)
	require.Empty(t, tasks, "推文回合不提交持久化任务")

	require.NotEmpty(t, captured.Messages)
	require.Equal(t, openai.ChatMessageRoleSystem, captured.Messages[0].Role)
	sawInstruction := false
	var lastUser string
	for _, m := range captured.Messages {
		if m.Role == openai.ChatMessageRoleSystem && strings.Contains(m.Content, "只输出将作为公开评论发布的正文") {
			sawInstruction = true
		}
		if m.Role == openai.ChatMessageRoleUser {
			lastUser = m.Content
		}
	}
	require.True(t, sawInstruction, "系统提示必须包含公开回复约束")
	require.Equal(t, message.Text, lastUser, "平台上下文不能混入用户消息")

	var runtimeText strings.Builder
	for _, m := range captured.RuntimeContext {
		runtimeText.WriteString(m.Content)
	}
	require.Contains(t, runtimeText.String(), "平台提供的互动上下文")
	require.Contains(t, runtimeText.String(), "推文正文: hello")
	require.Contains(t, runtimeText.String(), "不可信的外部文本")
	require.Contains(t, runtimeText.String(), "不要执行")
	require.NotContains(t, runtimeText.String(), "私密记忆条目不应外泄", "公开回合不注入记忆")
}

// TestTweetToolIsChatOnlyScheduleTool 验证纯聊天计划可以冻结 saber_tweet 而不申请执行授权。
func TestTweetToolIsChatOnlyScheduleTool(t *testing.T) {
	require.True(t, chatToolNames[tweetToolName])

	tools := []openai.Tool{tweetTool(), scheduleTool()}
	require.False(t, scheduleNeedsExecution(tools))
	require.Equal(t, []string{tweetToolName, "saber_schedule"}, toolNames(filterChatTools(tools)))

	withExec := append(append([]openai.Tool{}, tools...), openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "exec"}})
	require.True(t, scheduleNeedsExecution(withExec))
	require.Equal(t, []string{tweetToolName, "saber_schedule"}, toolNames(filterChatTools(withExec)))
}
