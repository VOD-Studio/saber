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
