// Package memory 提供按用户与群作用域隔离的长期记忆。
//
// 记忆条目以 SQLite 独立文件持久化；作用域由接入端确认的可信身份推导，
// 不接受模型生成的用户、账号或房间参数。模型工具与文本命令共用同一套
// 读写规则，授权只在服务内部判定一次。
package memory

import (
	"errors"
	"strconv"
	"time"

	"rua.plus/saber/internal/chat"
)

// 作用域类型。
const (
	// ScopeUser 是单个用户的个人空间，只在接入端确认的本人私聊或本机终端注入。
	ScopeUser = "user"
	// ScopeGroup 是一个群的共享空间，只在该群内读写，线程不改变其边界。
	ScopeGroup = "group"
)

// TerminalPlatform 是服务端令牌与操作系统用户已确定身份的本机入口。
const TerminalPlatform = "terminal"

// 容量默认值。
const (
	// DefaultMaxChars 是每个空间的存储上限，按 Unicode 字符计。
	DefaultMaxChars = 2200
	// DefaultInjectBytes 是单次注入的字节上限。
	DefaultInjectBytes = 8192
)

// 变更动作。
const (
	// ActionAdd 新增条目。
	ActionAdd = "add"
	// ActionReplace 修改条目。
	ActionReplace = "replace"
	// ActionRemove 删除条目。
	ActionRemove = "remove"
)

// 变更状态。
const (
	// StatusPending 表示建议等待有写权限的成员确认。
	StatusPending = "pending"
	// StatusApplied 表示建议已应用。
	StatusApplied = "applied"
	// StatusRejected 表示建议已被拒绝。
	StatusRejected = "rejected"
)

var (
	// ErrFull 表示空间已达到存储上限，需要先整理或删除条目。
	ErrFull = errors.New("记忆空间已满")
	// ErrConflict 表示条目已被其他任务修改，需要重新读取后再改。
	ErrConflict = errors.New("记忆条目版本冲突")
	// ErrNotFound 表示条目不在当前作用域内，或已被删除。
	ErrNotFound = errors.New("记忆条目不存在")
	// ErrDuplicate 表示空间内已存在完全相同的条目内容。
	ErrDuplicate = errors.New("已存在相同内容的记忆条目")
	// ErrEmptyContent 表示条目正文为空。
	ErrEmptyContent = errors.New("记忆内容不能为空")
	// ErrScope 表示当前身份无法证明任何可信记忆空间。
	ErrScope = errors.New("当前会话无法确定可信记忆空间")
	// ErrPaused 表示当前记忆空间的自动学习已暂停。
	ErrPaused = errors.New("当前记忆空间的自动学习已暂停")
	// ErrQuotaExceeded 表示超出记忆复盘配额。
	ErrQuotaExceeded = errors.New("超出记忆复盘配额")
	// ErrSkillNotFound 表示技能不存在。
	ErrSkillNotFound = errors.New("技能不存在")
	// ErrSkillConflict 表示技能版本冲突。
	ErrSkillConflict = errors.New("技能版本冲突")
	// ErrSkillDuplicate 表示已存在同名技能。
	ErrSkillDuplicate = errors.New("已存在同名技能")
	// ErrInvalidSkillName 表示技能标识名不合法。
	ErrInvalidSkillName = errors.New("技能标识必须由 1-64 位小写字母、数字、连字符、下划线或点组成")
	// ErrEmptySkillDescription 表示技能描述为空。
	ErrEmptySkillDescription = errors.New("技能描述不能为空")
)

// Scope 是记忆的隔离键，由可信身份推导，不能由模型参数指定。
type Scope struct {
	// Kind 是 ScopeUser 或 ScopeGroup。
	Kind string
	// Platform 是接入端名称。
	Platform string
	// Account 是接入账号的稳定标识。
	Account string
	// ID 在个人空间是发送者标识，在群空间是平台原生会话标识。
	ID string
}

// Key 编码完整作用域，避免分隔符出现在原始 ID 中时产生碰撞。
func (s Scope) Key() string {
	return "[" + strconv.Quote(s.Kind) + "," + strconv.Quote(s.Platform) + "," + strconv.Quote(s.Account) + "," + strconv.Quote(s.ID) + "]"
}

// Validate 拒绝缺少作用域字段的键。
func (s Scope) Validate() error {
	if (s.Kind != ScopeUser && s.Kind != ScopeGroup) || s.Platform == "" || s.Account == "" || s.ID == "" {
		return errors.New("memory scope requires kind, platform, account and id")
	}
	return nil
}

// String 返回可展示的作用域名称。
func (s Scope) String() string {
	if s.Kind == ScopeUser {
		return "个人空间"
	}
	return "本群共享空间"
}

// Space 从可信身份推导当前记忆空间，不接受模型提供的用户、账号或房间参数。
//
// 私聊与本机终端使用个人空间；其他会话使用按房间聚合的群共享空间，
// 线程不扩大也不缩小群作用域。身份不完整时返回 false，调用方不得注入个人记忆。
func Space(identity chat.Identity) (Scope, bool) {
	if identity.Session.Validate() != nil || identity.SenderID == "" {
		return Scope{}, false
	}
	if identity.Direct || identity.Session.Platform == TerminalPlatform {
		return Scope{Kind: ScopeUser, Platform: identity.Session.Platform, Account: identity.Session.Account, ID: identity.SenderID}, true
	}
	return Scope{Kind: ScopeGroup, Platform: identity.Session.Platform, Account: identity.Session.Account, ID: identity.Session.Conversation}, true
}

// Source 描述条目或建议的来源，用于审计与去重。
type Source struct {
	// Creator 是提出者标识，来自接入端确认的身份。
	Creator string
	// Task 是来源任务编号，零值表示直接命令写入。
	Task int64
	// Message 是来源消息 ID。
	Message string
}

// Entry 是一条已生效的长期记忆。
type Entry struct {
	// ID 在空间内稳定，用于修改和删除。
	ID int64
	// Content 是条目正文。
	Content string
	// Version 每次修改递增，用于发现并发覆盖。
	Version int64
	// Explicit 表示由用户明确要求保存，注入时优先。
	Explicit bool
	// Creator 是提出者标识。
	Creator string
	// SourceTask 是来源任务编号。
	SourceTask int64
	// SourceMessage 是来源消息 ID。
	SourceMessage string
	// CreatedAt 是创建时间。
	CreatedAt time.Time
	// UpdatedAt 是最后修改时间。
	UpdatedAt time.Time
}

// Change 是群空间待确认的记忆变更建议。
type Change struct {
	// ID 是建议编号。
	ID int64
	// Action 是 ActionAdd、ActionReplace 或 ActionRemove。
	Action string
	// EntryID 是 replace 与 remove 的目标条目。
	EntryID int64
	// ExpectedVersion 是提出建议时看到的条目版本。
	ExpectedVersion int64
	// Content 是 add 与 replace 的候选正文。
	Content string
	// Proposer 是提出者标识。
	Proposer string
	// SourceTask 是来源任务编号。
	SourceTask int64
	// SourceMessage 是来源消息 ID。
	SourceMessage string
	// Status 是 pending、applied 或 rejected。
	Status string
	// CreatedAt 是提出时间。
	CreatedAt time.Time
	// DecidedAt 是确认或拒绝时间，零值表示尚未处理。
	DecidedAt time.Time
}

// Mutation 是一次写入请求的结果。
type Mutation struct {
	// Entry 在直接写入时返回生效条目。
	Entry Entry
	// Change 在形成待确认建议时返回建议。
	Change Change
	// Suggested 表示本次没有直接生效，而是形成建议等待确认。
	Suggested bool
	// Duplicate 表示空间内已有相同内容，未新增条目。
	Duplicate bool
}

// Usage 描述一个空间的容量占用。
type Usage struct {
	// Scope 是统计的空间。
	Scope Scope
	// Count 是条目数量。
	Count int
	// Chars 是全部条目的字符数。
	Chars int
	// MaxChars 是空间存储上限。
	MaxChars int
}

// HistoryRecord 是可检索的历史对话投影记录。
type HistoryRecord struct {
	// ID 是在 memory.db 中的稳定唯一自增标识。
	ID int64
	// TaskID 是来源任务编号。
	TaskID int64
	// Scope 是所属的隔离空间。
	Scope Scope
	// Platform 是接入端平台。
	Platform string
	// Account 是接入账号标识。
	Account string
	// Conversation 是平台原生会话或房间。
	Conversation string
	// Thread 是话题或线程标识。
	Thread string
	// SenderID 是消息发送人标识。
	SenderID string
	// UserMessageID 是来源消息标识。
	UserMessageID string
	// UserText 是本轮用户输入正文。
	UserText string
	// AssistantText 是公开回答正文。
	AssistantText string
	// TaskStatus 是任务终态状态（如 completed, failed, cancelled, interrupted）。
	TaskStatus string
	// CreatedAt 是记录创建时间。
	CreatedAt time.Time
}

// HistorySearchOptions 是历史检索的参数。
type HistorySearchOptions struct {
	// Query 是搜索关键词。
	Query string
	// Limit 限制返回记录数量；零值或负值使用默认值 5，上限 20。
	Limit int
	// Cursor 是分页游标（上一页最后一条记录的 ID，返回 ID 小于该值的更早记录）。
	Cursor int64
}

// HistorySearchResult 是历史检索的结果。
type HistorySearchResult struct {
	// Scope 是检索的作用域空间。
	Scope Scope
	// Query 是实际执行的检索词。
	Query string
	// Records 是匹配的历史记录列表（按时间倒序）。
	Records []HistoryRecord
	// NextCursor 是下一页游标，没有更多数据时为 0。
	NextCursor int64
	// HasMore 表示是否还有更早的历史记录。
	HasMore bool
}

// HistoryContextOptions 是读取指定记录及其前后文的参数。
type HistoryContextOptions struct {
	// ID 是目标记录编号。
	ID int64
	// Before 是向前读取的条数（较早的记录），上限 10。
	Before int
	// After 是向后读取的条数（较新的记录），上限 10。
	After int
}

// HistoryContextResult 是上下文读取结果。
type HistoryContextResult struct {
	// Scope 是所属空间。
	Scope Scope
	// Target 是目标记录。
	Target HistoryRecord
	// Before 是目标之前的记录（按时间升序排列）。
	Before []HistoryRecord
	// After 是目标之后的记录（按时间升序排列）。
	After []HistoryRecord
}

// ProjectionInput 是写入历史投影的输入。
type ProjectionInput struct {
	// TaskID 是来源任务编号。
	TaskID int64
	// Scope 是目标空间，必须由可信身份推导。
	Scope Scope
	// Conversation 是平台会话/房间。
	Conversation string
	// Thread 是话题或线程标识。
	Thread string
	// SenderID 是发言人标识。
	SenderID string
	// UserMessageID 是用户消息标识。
	UserMessageID string
	// UserText 是用户正文。
	UserText string
	// AssistantText 是公开回答正文。
	AssistantText string
	// TaskStatus 是任务终态状态。
	TaskStatus string
	// CreatedAt 是任务创建时间。
	CreatedAt time.Time
}

// ScopeSettings 记录一个记忆空间的设置与复盘统计。
type ScopeSettings struct {
	// Scope 是所属空间。
	Scope Scope
	// Paused 表示该空间是否暂停自动学习。
	Paused bool
	// ReviewCount 是该空间已执行的复盘总次数。
	ReviewCount int
	// TokenCount 是该空间复盘累计消耗的 token 数量。
	TokenCount int
	// LastReviewedTaskID 是该空间最后复盘的任务编号。
	LastReviewedTaskID int64
	// UpdatedAt 是最后更新时间。
	UpdatedAt time.Time
}

// ScopeStatus 描述一个空间的综合状态，包含条目容量、学习开关、复盘统计与待确认建议数。
type ScopeStatus struct {
	// Usage 是容量占用。
	Usage Usage
	// Paused 表示是否暂停自动学习。
	Paused bool
	// ReviewCount 是该空间复盘总次数。
	ReviewCount int
	// TokenCount 是该空间复盘累计消耗的 token 数。
	TokenCount int
	// PendingCount 是当前空间未处理的待确认建议数。
	PendingCount int
}

// ProposalInput 是后台复盘或用户提出建议的输入。
type ProposalInput struct {
	// Action 是 ActionAdd、ActionReplace 或 ActionRemove。
	Action string
	// EntryID 在 replace 与 remove 时指定目标条目。
	EntryID int64
	// ExpectedVersion 是目标条目的预期版本，用于防止并发冲突。
	ExpectedVersion int64
	// Content 是 add 与 replace 的正文。
	Content string
	// Source 是建议的来源审计信息。
	Source Source
}

// SkillEntry 表示一条已持久化的程序性经验（技能）。
type SkillEntry struct {
	// ID 是技能的内部稳定编号。
	ID int64
	// Scope 是所属隔离空间。
	Scope Scope
	// Name 是技能唯一标识（Slug，如 deploy-service、k8s-pod-troubleshoot）。
	Name string
	// Description 是技能的触发场景与用途描述，用于轻量目录展示与模型路由。
	Description string
	// Content 是技能的完整 Markdown 正文，包含标准步骤、排错清单与注意事项。
	Content string
	// Version 是乐观锁版本号，更新或删除时用于并发冲突检测。
	Version int64
	// Creator 是创建者标识。
	Creator string
	// SourceTask 是产生该技能的任务编号。
	SourceTask int64
	// CreatedAt 是技能创建时间。
	CreatedAt time.Time
	// UpdatedAt 是技能最后更新时间。
	UpdatedAt time.Time
}

// SkillChange 表示群共享空间或后台复盘提出的技能变更建议。
type SkillChange struct {
	// ID 是建议编号。
	ID int64
	// Scope 是建议所属空间。
	Scope Scope
	// Action 是 ActionAdd、ActionReplace 或 ActionRemove。
	Action string
	// SkillID 在 replace 与 remove 时记录目标技能编号。
	SkillID int64
	// ExpectedVersion 是目标技能的预期版本，用于审批时防止并发覆盖。
	ExpectedVersion int64
	// Name 是技能标识名。
	Name string
	// Description 是技能描述。
	Description string
	// Content 是技能正文。
	Content string
	// Proposer 是建议提出者。
	Proposer string
	// SourceTask 是产生该建议的任务编号。
	SourceTask int64
	// Status 是建议状态（StatusPending、StatusApplied、StatusRejected）。
	Status string
	// CreatedAt 是建议提出时间。
	CreatedAt time.Time
	// DecidedAt 是建议审批时间。
	DecidedAt time.Time
}

// SkillMutation 汇总技能变更的执行结果。
type SkillMutation struct {
	// Skill 是直接生效的技能条目。
	Skill SkillEntry
	// Change 是处于待确认状态的变更建议。
	Change SkillChange
	// Suggested 为 true 时表示操作已转为待确认建议，尚未真正生效。
	Suggested bool
	// Duplicate 为 true 时表示内容已完全相同，未做重复写入。
	Duplicate bool
}
