package matrix

import (
	"context"
	"encoding/json"
	"errors"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/chat"
)

// 编译期断言：Matrix adapter 支持任务文件交付。
var _ chat.FileAdapter = (*ChatAdapter)(nil)

// UploadFile 上传加密文件快照，载荷是可重放的 Matrix 消息内容（含密钥与 mxc 地址）。
func (a *ChatAdapter) UploadFile(ctx context.Context, upload chat.FileUpload) ([]byte, error) {
	if err := a.validate(ctx, upload.Session); err != nil {
		return nil, err
	}
	content, err := a.service.UploadTaskFile(ctx, upload.Name, upload.Data, id.EventID(upload.ReplyTo), id.EventID(upload.Session.Thread))
	if err != nil {
		return nil, err
	}
	return json.Marshal(content)
}

// SendUploadedFile 用固定事务 ID 发送已上传的文件，不再次上传。
func (a *ChatAdapter) SendUploadedFile(ctx context.Context, reply chat.Reply, payload []byte) (string, error) {
	if err := a.validate(ctx, reply.Session); err != nil {
		return "", err
	}
	var content event.MessageEventContent
	if err := json.Unmarshal(payload, &content); err != nil {
		return "", errors.New("matrix 文件载荷无效: " + err.Error())
	}
	eventID, err := a.service.SendUploadedTaskFile(ctx, id.RoomID(reply.Session.Conversation), &content, reply.TransactionID)
	return string(eventID), err
}
