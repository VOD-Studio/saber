package ai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// outboxSuffix 追加在状态文件路径之后，待发队列与状态文件同目录。
	outboxSuffix = ".outbox.json"
	// maxOutboxAttempts 是自动重试上限：失败次数累计到这里的条目只保留在文件中，
	// 不再自动重发，也不会被悄悄删除。
	maxOutboxAttempts = 5
)

// outboxEntry 是一条待发的主动消息。
//
// ID 用于删除与累计失败次数；CreatedAt 决定启动补发顺序；Attempts 只统计失败次数，
// 新建条目为 0，第一次发送失败后变为 1。
type outboxEntry struct {
	ID        string    `json:"id"`
	Room      string    `json:"room"`
	Text      string    `json:"text"`
	Notice    bool      `json:"notice"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
}

// proactiveOutbox 是主动消息的最小持久化待发队列。
//
// 投递语义是至少一次（at-least-once）：条目在调用平台发送之前追加并 fsync，发送成功后
// 删除，失败则保留并累计 Attempts。进程在「平台已发送成功、条目尚未删除」之间崩溃时，
// 重启后会重发同一条消息，因此这个窗口内可能出现一次重复投递；反过来，条目在发送前
// 已经落盘，所以不会因为崩溃而丢失一条尚未发出的消息。达到 maxOutboxAttempts 的条目
// 保留在文件中作为死信，不再自动重试，也不会被静默丢弃。
//
// 所有文件访问都由 mu 串行化，调用方可以并发入队、删除与累计失败次数。
type proactiveOutbox struct {
	mu      sync.Mutex
	path    string
	entries []outboxEntry
}

// newProactiveOutbox 打开待发队列：文件不存在表示首次使用，损坏时返回错误而不是丢弃条目。
func newProactiveOutbox(path string) (*proactiveOutbox, error) {
	outbox := &proactiveOutbox{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return outbox, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取待发队列失败：%w", err)
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &outbox.entries); err != nil {
			return nil, fmt.Errorf("解析待发队列失败：%w", err)
		}
	}
	return outbox, nil
}

// enqueue 在发送前把消息写入磁盘；写盘失败时调用方不应继续发送，避免出现无法追溯的投递。
func (o *proactiveOutbox) enqueue(room, text string, notice bool) (outboxEntry, error) {
	entry := outboxEntry{ID: newOutboxID(), Room: room, Text: text, Notice: notice, CreatedAt: time.Now()}
	o.mu.Lock()
	defer o.mu.Unlock()
	previous := o.entries
	o.entries = append(append(make([]outboxEntry, 0, len(previous)+1), previous...), entry)
	if err := o.persistLocked(); err != nil {
		o.entries = previous
		return outboxEntry{}, err
	}
	return entry, nil
}

// remove 删除已成功投递的条目；写盘失败时条目继续保留，重启后会重发而不是丢失。
func (o *proactiveOutbox) remove(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := make([]outboxEntry, 0, len(o.entries))
	for _, entry := range o.entries {
		if entry.ID != id {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(o.entries) {
		return nil
	}
	previous := o.entries
	o.entries = kept
	if err := o.persistLocked(); err != nil {
		o.entries = previous
		return err
	}
	return nil
}

// bump 累计一条条目的失败次数并落盘，供下次启动判断是否还值得重试。
func (o *proactiveOutbox) bump(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for index := range o.entries {
		if o.entries[index].ID != id {
			continue
		}
		previous := o.entries[index].Attempts
		o.entries[index].Attempts++
		if err := o.persistLocked(); err != nil {
			o.entries[index].Attempts = previous
			return err
		}
		return nil
	}
	return nil
}

// snapshot 返回按创建顺序排列的条目副本，供启动补发遍历。
func (o *proactiveOutbox) snapshot() []outboxEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]outboxEntry(nil), o.entries...)
}

// persistLocked 先写临时文件、fsync 后改名，保证文件要么是旧内容要么是完整的新内容。
func (o *proactiveOutbox) persistLocked() error {
	entries := o.entries
	if entries == nil {
		entries = []outboxEntry{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化待发队列失败：%w", err)
	}
	dir := filepath.Dir(o.path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建待发队列目录失败：%w", err)
	}
	temp, err := os.CreateTemp(dir, ".outbox-*")
	if err != nil {
		return fmt.Errorf("创建待发队列临时文件失败：%w", err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err = temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("设置待发队列权限失败：%w", err)
	}
	if _, err = temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("写入待发队列失败：%w", err)
	}
	if err = temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("同步待发队列失败：%w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("关闭待发队列临时文件失败：%w", err)
	}
	if err = os.Rename(temp.Name(), o.path); err != nil {
		return fmt.Errorf("替换待发队列失败：%w", err)
	}
	return nil
}

// newOutboxID 生成条目标识；随机源不可用时退回时间戳，标识只需要唯一。
func newOutboxID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
