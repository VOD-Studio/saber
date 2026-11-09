package platform

import (
	"context"
	"fmt"
	"sync"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

// ReplyPager 保存真实分段消息 ID，使长回复的增量更新和最终定稿不会截断或重复发送尾段。
// 零值可用；同一 adapter 持有一个实例，不跨账号共享。
type ReplyPager struct {
	mu       sync.Mutex
	messages map[string][]string
}

// SplitMessage 在字符边界分段，短消息不做 rune 拷贝。
func SplitMessage(text string, limit int) []string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	result := make([]string, 0, utf8.RuneCountInString(text)/limit+1)
	start, count := 0, 0
	for offset := range text {
		if count == limit {
			result = append(result, text[start:offset])
			start = offset
			count = 0
		}
		count++
	}
	if start < len(text) {
		result = append(result, text[start:])
	}
	return result
}

// Send 逐段发送完整回复；首段引用原消息，后续段使用独立幂等键。
func (p *ReplyPager) Send(ctx context.Context, reply chat.Reply, text string, limit int, send func(context.Context, chat.Reply, string) (string, error)) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	parts := SplitMessage(text, limit)
	ids := make([]string, 0, len(parts))
	for i, part := range parts {
		item := reply
		if i > 0 {
			item.ReplyTo = ""
			if reply.TransactionID != "" {
				item.TransactionID = fmt.Sprintf("%s-%d", reply.TransactionID, i)
			}
		}
		id, err := send(ctx, item, part)
		if err != nil {
			if len(ids) > 0 {
				p.save(reply.Session, ids)
				return ids[0], err
			}
			return "", err
		}
		ids = append(ids, id)
	}
	p.save(reply.Session, ids)
	return ids[0], nil
}
func (p *ReplyPager) save(session chat.Session, ids []string) {
	if p.messages == nil {
		p.messages = make(map[string][]string)
	}
	if len(ids) > 0 && ids[0] != "" {
		p.messages[string(session.Key())+":"+ids[0]] = ids
	}
}

// Edit 更新每个已发送分段，并真实新增或删除变化的尾段。
func (p *ReplyPager) Edit(ctx context.Context, root string, reply chat.Reply, text string, limit int, send func(context.Context, chat.Reply, string) (string, error), edit func(context.Context, string, chat.Reply, string) error, remove func(context.Context, string, chat.Reply) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := string(reply.Session.Key()) + ":" + root
	ids := p.messages[key]
	if len(ids) == 0 {
		ids = []string{root}
	}
	parts := SplitMessage(text, limit)
	for i, part := range parts {
		item := reply
		if i > 0 {
			item.ReplyTo = ""
			if reply.TransactionID != "" {
				item.TransactionID = fmt.Sprintf("%s-%d", reply.TransactionID, i)
			}
		}
		if i < len(ids) {
			if err := edit(ctx, ids[i], item, part); err != nil {
				return err
			}
		} else {
			id, err := send(ctx, item, part)
			if err != nil {
				return err
			}
			ids = append(ids, id)
			p.save(reply.Session, ids)
		}
	}
	for len(ids) > len(parts) {
		last := len(ids) - 1
		if err := remove(ctx, ids[last], reply); err != nil {
			return err
		}
		ids = ids[:last]
	}
	p.save(reply.Session, ids)
	return nil
}
