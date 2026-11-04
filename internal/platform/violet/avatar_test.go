package violetplatform

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"testing"

	"rua.plus/saber/internal/chat"
)

func violetAvatarPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestPlatformAvatarRoundTripPersistsImageAndProfile(t *testing.T) {
	fake := newFakeViolet(t)
	fake.mu.Lock()
	fake.profile.AvatarURL = "/uploads/old-avatar"
	fake.mu.Unlock()
	p := New(newTestConfig(fake.endpoint()))
	var adapter chat.AvatarAdapter = p
	before, err := adapter.GetAvatar(context.Background())
	if err != nil || before.URL != fake.endpoint()+"/uploads/old-avatar" {
		t.Fatalf("读取旧头像失败: %+v %v", before, err)
	}
	data := violetAvatarPNG(t)
	updated, err := adapter.SetAvatar(context.Background(), data, "image/png", "saber.png")
	if err != nil || updated.URL == "" || updated.URL == before.URL {
		t.Fatalf("头像未替换: %+v %v", updated, err)
	}
	fresh, err := adapter.GetAvatar(context.Background())
	if err != nil || fresh != updated {
		t.Fatalf("GET 未读取已提交资料: %+v %+v %v", fresh, updated, err)
	}
	stored, err := p.api.downloadMedia(context.Background(), updated.URL)
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("头像未持久化原始字节: %d %v", len(stored), err)
	}
	fake.mu.Lock()
	profile := fake.profile
	calls := fake.avatarCalls
	fake.mu.Unlock()
	if calls != 1 || profile.UserID != testBotUserID || p.currentIdentity().AvatarURL != profile.AvatarURL {
		t.Fatalf("账号绑定或本地快照错误: %+v calls=%d", profile, calls)
	}
}

func TestPlatformAvatarGetRefreshesRemoteProfile(t *testing.T) {
	fake := newFakeViolet(t)
	p := New(newTestConfig(fake.endpoint()))
	initial, err := p.GetAvatar(context.Background())
	if err != nil || initial.URL != "" {
		t.Fatalf("未设置头像应为空: %+v %v", initial, err)
	}
	fake.mu.Lock()
	fake.profile.AvatarURL = "/uploads/changed-outside-saber"
	fake.mu.Unlock()
	fresh, err := p.GetAvatar(context.Background())
	if err != nil || fresh.URL != fake.endpoint()+"/uploads/changed-outside-saber" {
		t.Fatalf("不应返回缓存旧头像: %+v %v", fresh, err)
	}
}

func TestPlatformAvatarFailedCommitKeepsPreviousProfile(t *testing.T) {
	fake := newFakeViolet(t)
	fake.mu.Lock()
	fake.profile.AvatarURL = "/uploads/old-avatar"
	fake.avatarFailure = http.StatusInternalServerError
	fake.mu.Unlock()
	p := New(newTestConfig(fake.endpoint()))
	before, err := p.GetAvatar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SetAvatar(context.Background(), violetAvatarPNG(t), "image/png", "new.png")
	if clientStatus(err) != http.StatusInternalServerError {
		t.Fatalf("未保留服务端失败状态: %v", err)
	}
	after, err := p.GetAvatar(context.Background())
	if err != nil || before != after || p.currentIdentity().AvatarURL != "/uploads/old-avatar" {
		t.Fatalf("失败误改头像: %+v %+v %v", before, after, err)
	}
	if len(fake.uploadedMediaRecords()) != 1 {
		t.Fatal("应区分上传成功与资料提交失败")
	}
}

func TestPlatformAvatarRejectsInvalidImagesBeforeHTTP(t *testing.T) {
	fake := newFakeViolet(t)
	p := New(newTestConfig(fake.endpoint()))
	for _, tc := range []struct {
		name string
		data []byte
		mime string
	}{
		{"empty", nil, "image/png"},
		{"too large", make([]byte, maxMediaBytes+1), "image/png"},
		{"unsupported", []byte("<svg/>"), "image/svg+xml"},
		{"mismatch", violetAvatarPNG(t), "image/jpeg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := p.SetAvatar(context.Background(), tc.data, tc.mime, "image"); err == nil {
				t.Fatal("非法头像必须拒绝")
			}
		})
	}
	fake.mu.Lock()
	calls := fake.avatarCalls
	fake.mu.Unlock()
	if calls != 0 {
		t.Fatalf("非法头像已打到服务端: %d", calls)
	}
}

func TestPlatformAvatarAuthenticationAndCancellation(t *testing.T) {
	fake := newFakeViolet(t)
	p := New(newTestConfig(fake.endpoint()))
	p.api.token = "invalid"
	if _, err := p.GetAvatar(context.Background()); clientStatus(err) != http.StatusUnauthorized {
		t.Fatalf("应传播鉴权错误: %v", err)
	}
	if _, err := p.SetAvatar(context.Background(), violetAvatarPNG(t), "image/png", "avatar.png"); clientStatus(err) != http.StatusUnauthorized {
		t.Fatalf("写入应传播鉴权错误: %v", err)
	}
	p.api.token = testToken
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.SetAvatar(ctx, violetAvatarPNG(t), "image/png", "avatar.png"); err == nil {
		t.Fatal("已取消上下文不应完成更新")
	}
	var unconfigured Platform
	if _, err := unconfigured.GetAvatar(context.Background()); err == nil {
		t.Fatal("未配置平台不能读头像")
	}
	if _, err := unconfigured.SetAvatar(context.Background(), violetAvatarPNG(t), "image/png", "avatar.png"); err == nil {
		t.Fatal("未配置平台不能写头像")
	}
}
