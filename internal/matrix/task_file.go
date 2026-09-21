package matrix

import (
	"context"
	"errors"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/chat"
)

// SendTaskFile 上传任务文件快照并引用原消息；固定事务 ID 使汇报重试不会重复发文件。
// 上传内容始终加密，解密信息由房间消息携带，E2EE 房间由客户端继续加密消息事件。
func (s *CommandService) SendTaskFile(ctx context.Context, roomID id.RoomID, name string, data []byte, transactionID string, replyTo, thread id.EventID) (id.EventID, error) {
	if len(data) > 16*1024*1024 {
		return "", errors.New("task artifact exceeds 16 MiB")
	}
	encrypted := attachment.NewEncryptedFile()
	ciphertext := append([]byte(nil), data...)
	encrypted.EncryptInPlace(ciphertext)
	upload, err := s.client.UploadBytesWithName(ctx, ciphertext, "application/octet-stream", name)
	if err != nil {
		return "", err
	}
	content := &event.MessageEventContent{MsgType: event.MsgFile, Body: name, FileName: name, Info: &event.FileInfo{MimeType: "application/octet-stream", Size: len(data)}, File: &event.EncryptedFileInfo{EncryptedFile: *encrypted, URL: upload.ContentURI.CUString()}, RelatesTo: chatReplyRelation(chat.Reply{Session: chat.Session{Thread: string(thread)}, ReplyTo: string(replyTo)})}
	response, err := s.client.SendMessageEvent(ctx, roomID, event.EventMessage, content, mautrix.ReqSendEvent{TransactionID: transactionID})
	if err != nil {
		return "", err
	}
	return response.EventID, nil
}
