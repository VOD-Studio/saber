package matrixplatform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
)

// 确保房间包装满足 ai 主动聊天的平台端口。
var _ ai.ProactiveRooms = (*Rooms)(nil)

// TestRoomsMissingService 验证未装配房间服务时返回错误而不是 panic。
func TestRoomsMissingService(t *testing.T) {
	rooms := NewRooms(nil)
	ctx := context.Background()

	if _, err := rooms.ListConversations(ctx); err == nil {
		t.Error("ListConversations() 应返回错误")
	}
	if _, err := rooms.ConversationInfo(ctx, "!room:local"); err == nil {
		t.Error("ConversationInfo() 应返回错误")
	}
	if _, err := rooms.SendText(ctx, "!room:local", "你好"); err == nil {
		t.Error("SendText() 应返回错误")
	}
	if _, err := rooms.SendNotice(ctx, "!room:local", "你好"); err == nil {
		t.Error("SendNotice() 应返回错误")
	}
}

// TestConversationInfoConversion 验证 Matrix 房间快照到通用会话元数据的映射。
func TestConversationInfoConversion(t *testing.T) {
	tests := []struct {
		name string
		info matrix.RoomInfo
		want chat.ConversationInfo
	}{
		{
			name: "有名称的加密群聊",
			info: matrix.RoomInfo{
				ID:          "!room:local",
				Name:        "测试房间",
				Alias:       "#test:local",
				MemberCount: 5,
				IsEncrypted: true,
			},
			want: chat.ConversationInfo{
				Conversation: "!room:local",
				Name:         "测试房间",
				MemberCount:  5,
				Encrypted:    true,
			},
		},
		{
			name: "未命名房间回退为房间 ID",
			info: matrix.RoomInfo{ID: "!anon:local"},
			want: chat.ConversationInfo{
				Conversation: "!anon:local",
				Name:         "!anon:local",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conversationInfo(tt.info); got != tt.want {
				t.Errorf("conversationInfo() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestConversationInfoFieldDrift 守住双份房间模型的字段漂移：
// matrix.RoomInfo 新增或改名字段时，必须显式选择“映射到通用会话元数据”还是“刻意丢弃”，
// 避免新增能力在 shim 里静默漏掉。
func TestConversationInfoFieldDrift(t *testing.T) {
	mapped := map[string]bool{
		"ID":          true,
		"Name":        true,
		"MemberCount": true,
		"IsEncrypted": true,
	}
	dropped := map[string]bool{
		"Alias":     true, // 主动聊天只需要会话标识与展示名
		"Topic":     true,
		"AvatarURL": true,
	}

	typ := reflect.TypeOf(matrix.RoomInfo{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !mapped[name] && !dropped[name] {
			t.Errorf("matrix.RoomInfo 字段 %q 未登记：请加入 conversationInfo 映射或刻意丢弃清单", name)
		}
	}
	for name := range mapped {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("已映射字段 %q 在 matrix.RoomInfo 上已不存在，请同步 shim", name)
		}
	}
	for name := range dropped {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("丢弃字段 %q 在 matrix.RoomInfo 上已不存在，请清理丢弃清单", name)
		}
	}
}

// TestRoomsAgainstHomeserver 用假的 homeserver 验证端口确实委托给 Matrix 房间服务。
func TestRoomsAgainstHomeserver(t *testing.T) {
	var lastBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/joined_rooms"):
			_, _ = io.WriteString(w, `{"joined_rooms":["!room:local"]}`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/send/m.room.message/"):
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &lastBody)
			_, _ = io.WriteString(w, `{"event_id":"$sent:local"}`)
		default:
			// 房间元数据的每个状态事件都是独立查询，失败时由底层服务忽略，
			// 因此这里统一返回错误即可得到零值快照。
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"errcode":"M_UNKNOWN"}`)
		}
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Matrix.Homeserver = server.URL
	cfg.Matrix.UserID = "@bot:local"
	cfg.Matrix.AccessToken = "test-token"

	client, err := matrix.NewMatrixClient(&cfg.Matrix)
	if err != nil {
		t.Fatalf("matrix.NewMatrixClient() error = %v", err)
	}
	rooms := NewRooms(matrix.NewRoomService(client))
	ctx := context.Background()

	conversations, err := rooms.ListConversations(ctx)
	if err != nil {
		t.Fatalf("ListConversations() error = %v", err)
	}
	if len(conversations) != 1 || conversations[0].Conversation != "!room:local" {
		t.Fatalf("ListConversations() = %+v, want 单个 !room:local", conversations)
	}
	if conversations[0].Name != "!room:local" {
		t.Errorf("Name = %q, want 回退为房间 ID", conversations[0].Name)
	}

	info, err := rooms.ConversationInfo(ctx, "!room:local")
	if err != nil {
		t.Fatalf("ConversationInfo() error = %v", err)
	}
	if info.Conversation != "!room:local" || info.MemberCount != 0 || info.Encrypted {
		t.Errorf("ConversationInfo() = %+v, want 零值元数据", info)
	}

	if _, err := rooms.ConversationInfo(ctx, ""); err == nil {
		t.Error("ConversationInfo(\"\") 应返回错误")
	}

	eventID, err := rooms.SendText(ctx, "!room:local", "你好")
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
	if eventID != "$sent:local" {
		t.Errorf("SendText() eventID = %q, want $sent:local", eventID)
	}
	if msgType, _ := lastBody["msgtype"].(string); msgType != "m.text" {
		t.Errorf("SendText() msgtype = %v, want m.text", lastBody["msgtype"])
	}

	eventID, err = rooms.SendNotice(ctx, "!room:local", "提醒")
	if err != nil {
		t.Fatalf("SendNotice() error = %v", err)
	}
	if eventID != "$sent:local" {
		t.Errorf("SendNotice() eventID = %q, want $sent:local", eventID)
	}
	if msgType, _ := lastBody["msgtype"].(string); msgType != "m.notice" {
		t.Errorf("SendNotice() msgtype = %v, want m.notice", lastBody["msgtype"])
	}
}
