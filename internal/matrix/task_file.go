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

// UploadTaskFile 上传加密快照，返回可持久化的消息内容；重试发送时复用上传及密钥。
func (s *CommandService) UploadTaskFile(ctx context.Context, name string, data []byte, replyTo, thread id.EventID) (*event.MessageEventContent, error) {
	if len(data) > 16*1024*1024 {
		return nil, errors.New("task artifact exceeds 16 MiB")
	}
	encrypted := attachment.NewEncryptedFile()
	ciphertext := append([]byte(nil), data...)
	encrypted.EncryptInPlace(ciphertext)
	upload, err := s.client.UploadBytesWithName(ctx, ciphertext, "application/octet-stream", name)
	if err != nil {
		return nil, err
	}
	return &event.MessageEventContent{MsgType: event.MsgFile, Body: name, FileName: name, Info: &event.FileInfo{MimeType: "application/octet-stream", Size: len(data)}, File: &event.EncryptedFileInfo{EncryptedFile: *encrypted, URL: upload.ContentURI.CUString()}, RelatesTo: chatReplyRelation(chat.Reply{Session: chat.Session{Thread: string(thread)}, ReplyTo: string(replyTo)})}, nil
}

// SendUploadedTaskFile 用固定事务 ID 发送已上传的文件，不再次上传或生成密钥。
func (s *CommandService) SendUploadedTaskFile(ctx context.Context, roomID id.RoomID, content *event.MessageEventContent, transactionID string) (id.EventID, error) {
	response, err := s.client.SendMessageEvent(ctx, roomID, event.EventMessage, content, mautrix.ReqSendEvent{TransactionID: transactionID})
	if err != nil {
		return "", err
	}
	return response.EventID, nil
}
