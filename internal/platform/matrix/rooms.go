// 本文件把 Matrix 房间服务包装成通用平台端口，供 ai 主动聊天消费。
package matrixplatform

import (
	"context"
	"errors"
	"fmt"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/matrix"
)

// Rooms 把 Matrix 房间概念翻译成 ai 主动聊天可消费的通用会话端口。
//
// 房间 ID、成员数与加密状态等平台细节只在这里收敛，
// ai 核心拿到的是字符串会话标识与 chat.ConversationInfo。
type Rooms struct {
	rooms *matrix.RoomService
}

// NewRooms 绑定 Matrix 房间服务；rooms 为空时所有调用返回错误而不是 panic。
func NewRooms(rooms *matrix.RoomService) *Rooms {
	return &Rooms{rooms: rooms}
}

// ListConversations 返回机器人已加入的房间列表。
func (r *Rooms) ListConversations(ctx context.Context) ([]chat.ConversationInfo, error) {
	service, err := r.service()
	if err != nil {
		return nil, err
	}

	infos, err := service.GetJoinedRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取 matrix 房间列表失败: %w", err)
	}

	conversations := make([]chat.ConversationInfo, 0, len(infos))
	for _, info := range infos {
		conversations = append(conversations, conversationInfo(info))
	}
	return conversations, nil
}

// ConversationInfo 返回单个房间的元数据；未命名时名称回退为房间 ID。
func (r *Rooms) ConversationInfo(ctx context.Context, conversationID string) (chat.ConversationInfo, error) {
	service, err := r.service()
	if err != nil {
		return chat.ConversationInfo{}, err
	}

	info, err := service.GetRoomInfo(ctx, conversationID)
	if err != nil {
		return chat.ConversationInfo{}, fmt.Errorf("获取 matrix 房间 %s 元数据失败: %w", conversationID, err)
	}
	if info == nil {
		return chat.ConversationInfo{}, fmt.Errorf("matrix 房间 %s 未返回元数据", conversationID)
	}
	return conversationInfo(*info), nil
}

// SendText 以普通消息向房间投递内容，返回事件 ID。
func (r *Rooms) SendText(ctx context.Context, conversationID, text string) (string, error) {
	service, err := r.service()
	if err != nil {
		return "", err
	}

	eventID, err := service.SendMessage(ctx, conversationID, text)
	return string(eventID), err
}

// SendNotice 以通知消息向房间投递内容，返回事件 ID。
func (r *Rooms) SendNotice(ctx context.Context, conversationID, text string) (string, error) {
	service, err := r.service()
	if err != nil {
		return "", err
	}

	eventID, err := service.SendNotice(ctx, conversationID, text)
	return string(eventID), err
}

// service 返回底层房间服务，未装配时给出明确错误。
func (r *Rooms) service() (*matrix.RoomService, error) {
	if r == nil || r.rooms == nil {
		return nil, errors.New("matrix 平台缺少房间服务，无法提供会话元数据")
	}
	return r.rooms, nil
}

// conversationInfo 把 Matrix 房间快照规范化为通用会话元数据。
func conversationInfo(info matrix.RoomInfo) chat.ConversationInfo {
	name := info.Name
	if name == "" {
		name = info.ID.String()
	}
	return chat.ConversationInfo{
		Conversation: info.ID.String(),
		Name:         name,
		MemberCount:  info.MemberCount,
		Encrypted:    info.IsEncrypted,
	}
}
