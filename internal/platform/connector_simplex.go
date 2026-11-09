package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"rua.plus/saber/internal/chat"
)

func (p *connector) decodeSimplex(ctx context.Context, event map[string]any) ([]chat.Message, error) {
	response := obj(event["resp"])
	if len(response) == 0 {
		response = event
	}
	kind := str(response["type"])
	if kind == "rcvFileDescrReady" {
		file := obj(response["rcvFileTransfer"])
		id := str(file["fileId"])
		if id != "" {
			p.receiveSimplexFile(ctx, id)
		}
		return nil, nil
	}
	var items []any
	switch kind {
	case "newChatItems":
		items = arr(response["chatItems"])
	case "newChatItem":
		items = []any{response["chatItem"]}
	case "rcvFileComplete":
		item := obj(response["chatItem"])
		file := obj(obj(item["chatItem"])["file"])
		id := str(file["fileId"])
		stored := p.state("simplex-pending:" + id)
		if stored != "" {
			var pending map[string]any
			if err := json.Unmarshal([]byte(stored), &pending); err != nil {
				return nil, err
			}
			m := obj(pending["chatItem"])
			m["file"] = file
			pending["chatItem"] = m
			items = []any{pending}
			if err := p.setState(ctx, "simplex-pending:"+id, ""); err != nil {
				return nil, err
			}
		} else {
			items = []any{item}
		}
	default:
		return nil, nil
	}
	result := make([]chat.Message, 0, len(items))
	for _, raw := range items {
		item := obj(raw)
		info, m := obj(item["chatInfo"]), obj(item["chatItem"])
		direction := str(obj(m["chatDir"])["type"])
		if direction == "directSnd" || direction == "groupSnd" {
			continue
		}
		direct := str(info["type"]) == "direct"
		sender := str(obj(info["contact"])["contactId"])
		target := sender
		if !direct {
			target = "group:" + str(obj(info["groupInfo"])["groupId"])
			sender = str(obj(obj(m["chatDir"])["groupMember"])["groupMemberId"])
		}
		content := obj(obj(m["content"])["msgContent"])
		msg := chat.Message{ID: str(obj(m["meta"])["itemId"]), SenderID: sender, Direct: direct, Text: str(content["text"]), Session: chat.Session{Conversation: target}}
		if err := p.incomingAllowed(msg); err != nil {
			continue
		}
		file := obj(m["file"])
		if str(file["fileName"]) != "" {
			path := str(obj(file["fileSource"])["filePath"])
			if path == "" {
				id := str(file["fileId"])
				if id == "" {
					return nil, errors.New("SimpleX 文件缺少 fileId")
				}
				if err := p.setState(ctx, "simplex-pending:"+id, jsonString(item)); err != nil {
					return nil, err
				}
				p.receiveSimplexFile(ctx, id)
				continue
			}
			attachment, err := p.sharedMedia(path, "", str(file["fileName"]), str(content["type"]))
			if err != nil {
				return nil, err
			}
			msg.Attachments = []chat.Attachment{attachment}
		}
		result = append(result, msg)
	}
	return result, nil
}
func (p *connector) receiveSimplexFile(ctx context.Context, id string) {
	p.mu.Lock()
	send := p.sendSocket
	p.mu.Unlock()
	if send == nil {
		return
	}
	go func() {
		_, err := send(ctx, map[string]any{"cmd": "/freceive " + id})
		if err != nil && ctx.Err() == nil {
			_ = p.setState(context.WithoutCancel(ctx), "simplex-transfer-error:"+id, fmt.Sprintf("真实文件接收失败: %s", p.redact(err.Error())))
		}
	}()
}
