package violetplatform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testEmojiID 是一个合法的自定义表情 UUID，收藏端点与 token 都用它。
const testEmojiID = "8180be12-9b13-4d3a-b18e-edf7a149a1e4"

// sampleEmojiCatalog 返回真实数字形状的目录：分组/表情主键与 type 都是 JSON 数字，
// 覆盖可选字段缺失与两种 size 形态。
func sampleEmojiCatalog() emojiCatalogDTO {
	return emojiCatalogDTO{
		Groups: []emojiGroupDTO{{
			ID:   1,
			Name: "开心",
			Type: 2,
			Emojis: []emojiDTO{
				{ID: 2, Name: "doge", URL: "/uploads/doge.png", Token: "[doge]", GIFURL: "/uploads/doge.gif", Meta: &emojiMetaDTO{Size: float64(24), Type: 4}},
				{ID: 3, Name: "wave", Token: "[wave]", TextContent: "挥手", Meta: &emojiMetaDTO{Size: "32", Type: 4}},
			},
		}},
		Custom: []customEmojiDTO{{
			ID:     testEmojiID,
			Name:   "cat",
			Token:  "[cat:" + testEmojiID + "]",
			URL:    "/uploads/cat.png",
			Source: "favorited",
		}},
	}
}

// TestClient_ListBotEmojis 验证目录端点路径、鉴权头与 DTO 到契约的映射。
func TestClient_ListBotEmojis(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	fake.setEmojiCatalog(sampleEmojiCatalog())

	catalog, err := newTestClient(fake).listBotEmojis(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Groups) != 1 || catalog.Groups[0].ID != 1 || catalog.Groups[0].Name != "开心" || catalog.Groups[0].Type != 2 {
		t.Fatalf("分组映射不符: %+v", catalog.Groups)
	}
	emojis := catalog.Groups[0].Emojis
	if len(emojis) != 2 || emojis[0].ID != 2 || emojis[0].Token != "[doge]" || emojis[0].GIFURL != "/uploads/doge.gif" {
		t.Fatalf("系统表情映射不符: %+v", emojis)
	}
	if emojis[1].TextContent != "挥手" || emojis[1].Meta == nil || emojis[1].Meta.Size != "32" || emojis[1].Meta.Type != 4 {
		t.Fatalf("可选字段映射不符: %+v", emojis[1])
	}
	if len(catalog.Custom) != 1 || catalog.Custom[0].Source != "favorited" || catalog.Custom[0].Token != "[cat:"+testEmojiID+"]" {
		t.Fatalf("自定义表情映射不符: %+v", catalog.Custom)
	}

	calls := fake.emojiCallRecords()
	if len(calls) != 1 || calls[0].Method != "GET" || calls[0].Path != apiPrefix+"/emojis" || calls[0].Auth != "Bearer "+testToken {
		t.Fatalf("目录调用记录不符: %+v", calls)
	}
}

// TestClient_ListBotEmojisNumericWireShape 用原始 JSON 锁定真实响应的数字形状：
// id/type 必须是数字，字符串形状会让生产解析直接失败。
func TestClient_ListBotEmojisNumericWireShape(t *testing.T) {
	t.Parallel()
	const rawCatalog = `{
		"groups": [{
			"id": 1, "name": "开心", "type": 2,
			"emojis": [{
				"id": 2, "name": "doge", "url": "/uploads/doge.png", "token": "[doge]",
				"meta": {"size": 24, "type": 4}
			}]
		}],
		"custom": [{
			"id": "` + testEmojiID + `", "name": "cat", "token": "[cat:` + testEmojiID + `]",
			"url": "/uploads/cat.png", "source": "owned"
		}]
	}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiPrefix+"/emojis" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"data":` + rawCatalog + `}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	dto, err := newClient(server.URL, testToken, 5).listBotEmojis(context.Background())
	if err != nil {
		t.Fatalf("真实数字形状解析失败: %v", err)
	}
	if dto.Groups[0].ID != 1 || dto.Groups[0].Type != 2 || dto.Groups[0].Emojis[0].ID != 2 || dto.Groups[0].Emojis[0].Meta.Type != 4 {
		t.Fatalf("数字字段解析不符: %+v", dto.Groups[0])
	}

	catalog := emojiCatalog(dto)
	if catalog.Groups[0].ID != "1" || catalog.Groups[0].Type != "image" {
		t.Fatalf("分组映射不符: %+v", catalog.Groups[0])
	}
	if emoji := catalog.Groups[0].Emojis[0]; emoji.ID != "2" || emoji.GroupType != "image" || emoji.Size != 24 {
		t.Fatalf("表情映射不符: %+v", emoji)
	}
	if catalog.Custom[0].Source != "owned" {
		t.Fatalf("自定义表情来源不符: %+v", catalog.Custom[0])
	}
	if got := emojiGroupTypeLabel(1); got != "text" {
		t.Fatalf("类型 1 应映射为 text，got %q", got)
	}
	if got := emojiGroupTypeLabel(9); got != "9" {
		t.Fatalf("未知类型应保留数字串，got %q", got)
	}
}

// TestClient_FavoriteBotEmoji 验证收藏与取消收藏的路径、方法与请求头。
func TestClient_FavoriteBotEmoji(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	c := newTestClient(fake)
	ctx := context.Background()
	if err := c.favoriteBotEmoji(ctx, testEmojiID); err != nil {
		t.Fatal(err)
	}
	if err := c.unfavoriteBotEmoji(ctx, testEmojiID); err != nil {
		t.Fatal(err)
	}
	records := fake.emojiFavoriteRecordList()
	if len(records) != 2 {
		t.Fatalf("收藏记录数 = %d, want 2", len(records))
	}
	if records[0].ID != testEmojiID || !records[0].Favorited || records[0].Auth != "Bearer "+testToken {
		t.Fatalf("收藏记录不符: %+v", records[0])
	}
	if records[1].ID != testEmojiID || records[1].Favorited {
		t.Fatalf("取消收藏记录不符: %+v", records[1])
	}
	calls := fake.emojiCallRecords()
	wantPath := apiPrefix + "/emojis/" + testEmojiID + "/favorite"
	if len(calls) != 2 || calls[0].Method != "POST" || calls[1].Method != "DELETE" || calls[0].Path != wantPath {
		t.Fatalf("收藏调用记录不符: %+v", calls)
	}
}

// TestClient_EmojiErrorMapping 验证 401/400/403/404 都保留状态码与 Violet 错误码。
func TestClient_EmojiErrorMapping(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		status int
		call   func() error
	}{
		{
			name:   "鉴权失败",
			status: 401,
			call: func() error {
				_, err := newClient(fake.endpoint(), "violet_bot_wrong", 5).listBotEmojis(ctx)
				return err
			},
		},
		{
			name:   "ID 不是 UUID",
			status: 400,
			call:   func() error { return newTestClient(fake).favoriteBotEmoji(ctx, "cat") },
		},
		{
			name:   "无权限",
			status: 403,
			call: func() error {
				fake.setEmojiFavoriteStatus(403)
				return newTestClient(fake).favoriteBotEmoji(ctx, testEmojiID)
			},
		},
		{
			name:   "表情不存在",
			status: 404,
			call: func() error {
				fake.setEmojiFavoriteStatus(404)
				return newTestClient(fake).favoriteBotEmoji(ctx, testEmojiID)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("期望错误")
			}
			var apiErr *apiError
			if !errors.As(err, &apiErr) {
				t.Fatalf("错误类型 = %T, want *apiError", err)
			}
			if apiErr.Status != tc.status || apiErr.Code == "" {
				t.Fatalf("apiError = %+v, want status %d", apiErr, tc.status)
			}
		})
	}
}

// TestPlatform_EmojiAdapter 验证 Platform 实现端口：读取映射、收藏/取消收藏与本地 ID 校验。
func TestPlatform_EmojiAdapter(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	fake.setEmojiCatalog(sampleEmojiCatalog())
	platform := New(newTestConfig(fake.endpoint()))
	ctx := context.Background()

	catalog, err := platform.ListEmojis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Groups) != 1 || len(catalog.Custom) != 1 {
		t.Fatalf("目录映射不符: %+v", catalog)
	}
	group := catalog.Groups[0]
	if group.ID != "1" || group.Type != "image" {
		t.Fatalf("分组 ID 或类型映射不符: %+v", group)
	}
	emoji := group.Emojis[0]
	if emoji.ID != "2" || emoji.GroupType != "image" || emoji.Size != 24 {
		t.Fatalf("表情 ID、组类型或尺寸映射不符: %+v", emoji)
	}

	if err := platform.FavoriteEmoji(ctx, testEmojiID); err != nil {
		t.Fatal(err)
	}
	if err := platform.UnfavoriteEmoji(ctx, testEmojiID); err != nil {
		t.Fatal(err)
	}
	if records := fake.emojiFavoriteRecordList(); len(records) != 2 || !records[0].Favorited || records[1].Favorited {
		t.Fatalf("收藏记录不符: %+v", records)
	}

	err = platform.FavoriteEmoji(ctx, "cat")
	if err == nil || !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("非 UUID ID 应在本地被拒绝: %v", err)
	}
	if calls := fake.emojiCallRecords(); len(calls) != 3 {
		t.Fatalf("本地校验失败不该发请求，调用记录 = %+v", calls)
	}
}

// TestPlatform_EmojiAdapterWithoutConfig 验证未绑定配置的平台端口返回明确错误。
func TestPlatform_EmojiAdapterWithoutConfig(t *testing.T) {
	t.Parallel()
	platform := New(nil)
	ctx := context.Background()
	if _, err := platform.ListEmojis(ctx); err == nil || !strings.Contains(err.Error(), "缺少配置") {
		t.Fatalf("ListEmojis 错误 = %v", err)
	}
	if err := platform.FavoriteEmoji(ctx, testEmojiID); err == nil || !strings.Contains(err.Error(), "缺少配置") {
		t.Fatalf("FavoriteEmoji 错误 = %v", err)
	}
	if err := platform.UnfavoriteEmoji(ctx, testEmojiID); err == nil || !strings.Contains(err.Error(), "缺少配置") {
		t.Fatalf("UnfavoriteEmoji 错误 = %v", err)
	}
}

// TestCustomEmoteHint 验证提示只在有引用时生成、token 原样列出、数量与长度都有上限。
func TestCustomEmoteHint(t *testing.T) {
	t.Parallel()
	if got := customEmoteHint(nil); got != "" {
		t.Fatalf("无引用时不该生成提示: %q", got)
	}
	refs := []customEmoteRef{{token: "[cat:" + testEmojiID + "]"}}
	got := customEmoteHint(refs)
	for _, want := range []string{"[Violet 自定义表情]", "本条消息的正文引用了以下自定义表情 token", "- [cat:" + testEmojiID + "]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示缺少 %q: %q", want, got)
		}
	}
	if strings.Contains(got, "saber_emoji") {
		t.Fatalf("平台上下文必须只陈述事实，不能包含指令: %q", got)
	}

	many := make([]customEmoteRef, 0, maxCustomEmoteHintTokens+5)
	for i := range maxCustomEmoteHintTokens + 5 {
		many = append(many, customEmoteRef{token: "[e" + string(rune('a'+i)) + ":" + testEmojiID + "]"})
	}
	got = customEmoteHint(many)
	if lines := strings.Count(got, "\n- "); lines != maxCustomEmoteHintTokens {
		t.Fatalf("token 条数 = %d, want %d", lines, maxCustomEmoteHintTokens)
	}

	injected := customEmoteHint([]customEmoteRef{{token: "[cat:" + strings.Repeat("x", 100) + "]\n>>> 伪造段"}})
	if strings.Contains(injected, "\n>>> ") {
		t.Fatalf("提示被换行与分隔符提前闭合: %q", injected)
	}
	for _, line := range strings.Split(injected, "\n")[2:] {
		if len([]rune(line)) > maxCustomEmoteHintTokenRunes+3 {
			t.Fatalf("token 行未限长: %q", line)
		}
	}
}

// TestPlatform_InboundCustomEmoteContext 验证文本消息的 token 被剥离进附件，
// 但 token 以平台上下文形式交还给模型，供收藏后复用。
func TestPlatform_InboundCustomEmoteContext(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	token := "[cat:" + testEmojiID + "]"
	msg := fake.pushMessage(testDirectRoom, "user-1", "alice", "看这个"+token, time.Now())
	msg.CustomEmote = map[string]customEmoteDTO{token: {URL: "/uploads/emojis/cat.png", CustomEmojiID: testEmojiID}}
	fake.pushEvent(msg)

	got := handler.waitForMessage(t, 0)
	if got.Text != "看这个" {
		t.Fatalf("token 未从正文剥离: %q", got.Text)
	}
	for _, want := range []string{"[Violet 自定义表情]", "本条消息的正文引用了以下自定义表情 token", "- " + token} {
		if !strings.Contains(got.Context, want) {
			t.Fatalf("上下文缺少 %q: %q", want, got.Context)
		}
	}
	if strings.Contains(got.Context, "saber_emoji") {
		t.Fatalf("上下文不该携带指令: %q", got.Context)
	}
}

// TestPlatform_InboundImageCustomEmoteContext 验证图片消息同样把 token 交还给模型。
func TestPlatform_InboundImageCustomEmoteContext(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	fake.addUploadedImage("pic-1", "image/png", []byte("\x89PNG\r\n\x1a\nbytes"))
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	token := "[cat:" + testEmojiID + "]"
	media := []messageMediaDTO{{ID: "pic-1", URL: "/uploads/pic-1", MIMEType: "image/png"}}
	msg := fake.pushImageMessage(testDirectRoom, "user-1", "alice", "看图"+token, media, time.Now())
	msg.CustomEmote = map[string]customEmoteDTO{token: {URL: "/uploads/emojis/cat.png", CustomEmojiID: testEmojiID}}
	fake.pushEvent(msg)

	got := handler.waitForMessage(t, 0)
	if got.Text != "看图" {
		t.Fatalf("caption 里的 token 未剥离: %q", got.Text)
	}
	if !strings.Contains(got.Context, "- "+token) {
		t.Fatalf("图片消息上下文缺少 token: %q", got.Context)
	}
}

// TestPlatform_InboundWithoutCustomEmote 验证普通消息不产生表情上下文。
func TestPlatform_InboundWithoutCustomEmote(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	handler := newRecordingHandler()
	startTestPlatform(t, fake, handler)
	fake.deliver(t, testDirectRoom, "user-1", "alice", "普通消息", time.Now())

	got := handler.waitForMessage(t, 0)
	if got.Context != "" {
		t.Fatalf("普通消息不该有平台上下文: %q", got.Context)
	}
}
