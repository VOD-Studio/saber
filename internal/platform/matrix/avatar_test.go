package matrixplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
)

// avatarHomeserver 保存资料与上传内容，验证实际 SDK HTTP 行为而非方法回声。
type avatarHomeserver struct {
	avatar        string
	uploaded      []byte
	uploads       int
	updates       int
	uploadFailure bool
	updateFailure bool
	readFailure   bool
}

func newAvatarPlatform(t *testing.T, state *avatarHomeserver, wantData []byte) *Platform {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer avatar-token" {
			t.Error("SDK 请求未使用机器人凭据")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/_matrix/media/v3/upload":
			state.uploads++
			if state.uploadFailure {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"errcode":"M_BAD_JSON","error":"upload rejected"}`)
				return
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("读取上传内容: %v", err)
			}
			state.uploaded = data
			if !bytes.Equal(data, wantData) {
				t.Error("头像没有按原始图片公开上传")
			}
			if r.Header.Get("Content-Type") != "image/png" || r.URL.Query().Get("filename") != "avatar.png" {
				t.Error("上传 MIME 或文件名不正确")
			}
			_, _ = io.WriteString(w, `{"content_uri":"mxc://matrix.test/new-avatar"}`)
		case r.URL.Path == "/_matrix/client/v3/profile/@saber:matrix.test/avatar_url":
			switch r.Method {
			case http.MethodGet:
				if state.readFailure {
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"errcode":"M_FORBIDDEN","error":"avatar read rejected"}`)
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]string{"avatar_url": state.avatar}); err != nil {
					t.Errorf("响应当前头像: %v", err)
				}
			case http.MethodPut:
				state.updates++
				if state.updateFailure {
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"errcode":"M_FORBIDDEN","error":"profile rejected"}`)
					return
				}
				var body struct {
					AvatarURL string `json:"avatar_url"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("解析资料更新: %v", err)
				}
				if body.AvatarURL != "mxc://matrix.test/new-avatar" || state.uploads != 1 {
					t.Error("资料未使用已上传的头像")
				}
				state.avatar = body.AvatarURL
				_, _ = io.WriteString(w, `{}`)
			default:
				t.Errorf("意外资料请求方法: %s", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		default:
			t.Errorf("请求操作了自身之外的目标或非公开上传路径: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := mautrix.NewClient(server.URL, "@saber:matrix.test", "avatar-token")
	if err != nil {
		t.Fatal(err)
	}
	client.Client = server.Client()
	client.DefaultHTTPRetries = 0
	return New(nil, nil, nil, WithAvatarClient(client))
}

func avatarPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestPlatform_AvatarSetThenRead(t *testing.T) {
	data := avatarPNG(t)
	state := &avatarHomeserver{avatar: "mxc://matrix.test/old-avatar"}
	p := newAvatarPlatform(t, state, data)
	before, err := p.GetAvatar(context.Background())
	if err != nil || before.URL != state.avatar {
		t.Fatalf("初始头像 = %+v, err = %v", before, err)
	}
	set, err := p.SetAvatar(context.Background(), data, "image/png", "avatar.png")
	if err != nil || set.URL != "mxc://matrix.test/new-avatar" {
		t.Fatalf("设置头像 = %+v, err = %v", set, err)
	}
	got, err := p.GetAvatar(context.Background())
	if err != nil || got != set || state.updates != 1 || !bytes.Equal(state.uploaded, data) {
		t.Fatalf("读取已更新头像 = %+v, err = %v, updates = %d", got, err, state.updates)
	}
}

func TestPlatform_AvatarFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		upload bool
		update bool
		want   string
		puts   int
	}{
		{name: "upload", upload: true, want: "upload rejected", puts: 0},
		{name: "profile", update: true, want: "profile rejected", puts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := avatarPNG(t)
			state := &avatarHomeserver{avatar: "mxc://matrix.test/old-avatar", uploadFailure: tc.upload, updateFailure: tc.update}
			p := newAvatarPlatform(t, state, data)
			got, err := p.SetAvatar(context.Background(), data, "image/png", "avatar.png")
			var httpErr mautrix.HTTPError
			if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.As(err, &httpErr) || got.URL != "" {
				t.Fatalf("错误未保留 SDK 原因: avatar = %+v, err = %v", got, err)
			}
			if state.updates != tc.puts || state.avatar != "mxc://matrix.test/old-avatar" {
				t.Fatalf("失败后资料发生变化: %+v", state)
			}
		})
	}
}

func TestPlatform_AvatarRejectsInvalidImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		mime string
	}{
		{name: "empty", mime: "image/png"},
		{name: "text", data: []byte("not an image"), mime: "image/png"},
		{name: "mismatch", data: avatarPNG(t), mime: "image/jpeg"},
		{name: "unsupported", data: []byte("<svg></svg>"), mime: "image/svg+xml"},
		{name: "too_large", data: make([]byte, 10*1024*1024+1), mime: "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &avatarHomeserver{}
			p := newAvatarPlatform(t, state, nil)
			if _, err := p.SetAvatar(context.Background(), tc.data, tc.mime, "avatar.png"); err == nil {
				t.Fatal("非法头像未拒绝")
			}
			if state.uploads != 0 || state.updates != 0 {
				t.Fatal("非法头像发出了上传或资料修改请求")
			}
		})
	}
}

func TestPlatform_AvatarReadFailure(t *testing.T) {
	state := &avatarHomeserver{readFailure: true}
	p := newAvatarPlatform(t, state, nil)
	got, err := p.GetAvatar(context.Background())
	var httpErr mautrix.HTTPError
	if err == nil || !errors.As(err, &httpErr) || !strings.Contains(err.Error(), "avatar read rejected") || got.URL != "" {
		t.Fatalf("读取失败没有保留 SDK 原因: avatar = %+v, err = %v", got, err)
	}
}
