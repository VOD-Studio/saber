package violetplatform

import (
	"sync"
	"time"
)

// 会话记忆与去重集的容量、有效期。
const (
	// defaultSeenCapacity 是去重集容量，够覆盖一次长时间断线补拉的消息量。
	defaultSeenCapacity = 512
	// defaultStateCapacity 是会话记忆容量上限，避免长期运行的机器人无限增长。
	defaultStateCapacity = 256
	// conversationKindTTL 是会话形态缓存有效期：形态几乎不变，但改名与成员变动
	// 不该让机器人拿着旧判断一直答话。
	conversationKindTTL = 5 * time.Minute
)

// seenMessages 是消息 ID 的有界去重集。
//
// SSE 与断线补拉必然重叠，不去重就会让模型把同一句话回答两遍；只保留最近若干条
// 即可：更早的消息再出现属于服务端异常，宁可漏答也不重复回答。
type seenMessages struct {
	mu    sync.Mutex
	cap   int
	ids   map[string]struct{}
	order []string
}

// newSeenMessages 构造去重集，capacity 非正时取默认值。
func newSeenMessages(capacity int) *seenMessages {
	if capacity <= 0 {
		capacity = defaultSeenCapacity
	}
	return &seenMessages{cap: capacity, ids: make(map[string]struct{}, capacity)}
}

// add 记录消息 ID，返回是否首次见到。空 ID 视为首次，交给调用方按无效消息处理。
func (s *seenMessages) add(id string) bool {
	if id == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return false
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	for len(s.order) > s.cap {
		evict := s.order[0]
		s.order = s.order[1:]
		delete(s.ids, evict)
	}
	return true
}

// size 返回当前记录条数，供测试确认淘汰生效。
func (s *seenMessages) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids)
}

// conversationMemory 是单个会话的进程内记忆：形态缓存与已处理到的时间水位。
//
// 状态自带锁，调用方从会话表取到指针后可以跨 goroutine 安全读写。
type conversationMemory struct {
	mu          sync.Mutex
	kind        string
	kindExpires time.Time
	watermark   time.Time
	baselined   bool
}

// cachedKind 返回仍在有效期内的会话形态，第二值为 false 表示需要重新查询。
func (m *conversationMemory) cachedKind(now time.Time) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kind == "" || !now.Before(m.kindExpires) {
		return "", false
	}
	return m.kind, true
}

// storedKind 返回已缓存的形态（可能已过期），用于查询失败时的降级判断。
func (m *conversationMemory) storedKind() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.kind
}

// rememberKind 写入会话形态及其有效期。
func (m *conversationMemory) rememberKind(kind string, expires time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kind = kind
	m.kindExpires = expires
}

// noteSeen 把水位推进到已处理消息的最大 created_at。
func (m *conversationMemory) noteSeen(created time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baselined = true
	if created.After(m.watermark) {
		m.watermark = created
	}
}

// since 返回补拉起点。
//
// 第二值为 false 表示该会话还没打过基线：此时绝不能回放历史，
// 否则每次启动都会把站内旧消息当新问题回答一遍。
func (m *conversationMemory) since(now time.Time) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.baselined {
		m.baselined = true
		m.watermark = now
		return m.watermark, false
	}
	return m.watermark, true
}

// conversationStates 按会话 ID 保管记忆，容量有界以免长期运行的机器人无限增长。
type conversationStates struct {
	mu      sync.Mutex
	items   map[string]*conversationMemory
	order   []string
	cap     int
	nowFunc func() time.Time
}

// newConversationStates 构造会话记忆表，capacity 非正时取默认值。
func newConversationStates(capacity int) *conversationStates {
	if capacity <= 0 {
		capacity = defaultStateCapacity
	}
	return &conversationStates{items: make(map[string]*conversationMemory, capacity), cap: capacity, nowFunc: time.Now}
}

// get 返回该会话的记忆，必要时创建。返回的指针在表内唯一。
func (c *conversationStates) get(conversationID string) *conversationMemory {
	c.mu.Lock()
	defer c.mu.Unlock()
	if memory, ok := c.items[conversationID]; ok {
		return memory
	}
	memory := &conversationMemory{}
	c.items[conversationID] = memory
	c.order = append(c.order, conversationID)
	for len(c.order) > c.cap {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.items, evict)
	}
	return memory
}

// known 返回当前已知的会话 ID 快照，供重连后逐个补拉。
func (c *conversationStates) known() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.items))
	for _, id := range c.order {
		if _, ok := c.items[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// now 返回当前时间，测试可替换以固定 TTL 判定。
func (c *conversationStates) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}
