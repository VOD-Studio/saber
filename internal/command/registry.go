// Package command 维护聊天命令的解析、执行与平台目录。
package command

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

// Argument 描述目录中一个命令参数。
type Argument struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// Descriptor 是可发布的命令信息；路径不带输入前缀。
type Descriptor struct {
	ID          string     `json:"id"`
	Path        []string   `json:"path"`
	Description string     `json:"description"`
	Arguments   []Argument `json:"arguments"`
	Scope       string     `json:"scope"`
}

// Handler 只接收可信接入端构造的消息与出站 adapter。raw 保留路径之后的原文。
type Handler func(context.Context, chat.Message, chat.Adapter, string) error

// Definition 将目录信息与执行规则放在一起，避免目录与实际命令漂移。
type Definition struct {
	Descriptor
	Aliases    [][]string
	Capability string
	Authorize  func(chat.Identity) bool
	Once       bool
	Handle     Handler
}

// Receipts 持久化有副作用命令的执行状态，重复投递只重试回执。
type Receipts interface {
	ClaimCommand(context.Context, chat.Message, string) (state, reply string, err error)
	CompleteCommand(context.Context, chat.Message, string, string) error
}

// Registry 是启动期间装配的命令集合；启动后只读。
type Registry struct {
	definitions []Definition
	paths       map[string]int
	maxDepth    int
	receipts    Receipts
}

// New 创建空注册表。
func New() *Registry { return &Registry{paths: make(map[string]int)} }

// SetReceipts 在开始接收消息前接入持久化回执存储。
func (r *Registry) SetReceipts(receipts Receipts) { r.receipts = receipts }

// Register 校验路径与别名，拒绝歧义定义。
func (r *Registry) Register(def Definition) error {
	if def.ID == "" || len(def.Path) == 0 || def.Handle == nil || def.Scope == "" {
		return errors.New("命令缺少 ID、路径、作用域或处理器")
	}
	for _, existing := range r.definitions {
		if existing.ID == def.ID {
			return fmt.Errorf("命令 ID %q 重复", def.ID)
		}
	}
	seen := make(map[string]bool)
	for _, path := range append([][]string{def.Path}, def.Aliases...) {
		if len(path) == 0 {
			return errors.New("空命令别名")
		}
		for _, segment := range path {
			if segment == "" || strings.ContainsAny(segment, " /!\t\r\n") {
				return fmt.Errorf("无效命令路径 %q", segment)
			}
		}
		key := strings.ToLower(strings.Join(path, " "))
		if _, exists := r.paths[key]; exists || seen[key] {
			return fmt.Errorf("命令路径 %q 重复", key)
		}
		seen[key] = true
	}
	index := len(r.definitions)
	for _, path := range append([][]string{def.Path}, def.Aliases...) {
		r.paths[strings.ToLower(strings.Join(path, " "))] = index
		r.maxDepth = max(r.maxDepth, len(path))
	}
	r.definitions = append(r.definitions, def)
	return nil
}

// Describe 返回该平台可执行的目录快照。
func (r *Registry) Describe(capabilities map[string]bool) []Descriptor {
	result := make([]Descriptor, 0, len(r.definitions))
	for _, def := range r.definitions {
		if (def.Capability == "" || capabilities[def.Capability]) && catalogID(def.ID) && catalogPath(def.Path) {
			result = append(result, def.Descriptor)
		}
	}
	slices.SortFunc(result, func(a, b Descriptor) int {
		return strings.Compare(strings.Join(a.Path, " "), strings.Join(b.Path, " "))
	})
	return result
}

func catalogPath(path []string) bool {
	if len(path) == 0 || len(path) > 4 {
		return false
	}
	for _, part := range path {
		if len(part) == 0 || len(part) > 80 || !asciiLetter(part[0]) {
			return false
		}
		for i := 1; i < len(part); i++ {
			if !catalogByte(part[i]) {
				return false
			}
		}
	}
	return true
}

func catalogID(id string) bool {
	if len(id) == 0 || len(id) > 128 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 1; i < len(id); i++ {
		if !catalogByte(id[i]) {
			return false
		}
	}
	return true
}

func asciiLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func catalogByte(c byte) bool {
	return asciiLetter(c) || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-'
}

// Parse 只识别正文开头的命令；转义的 // 和非命令正文均返回 ok=false。
// rests[i] 保留第 i 个词后跳过一个分隔符的原文，供处理器读取未改写参数。
func Parse(text string) (words, rests []string, prefix byte, ok bool) {
	if len(text) < 2 || text[0] != '/' && text[0] != '!' || strings.HasPrefix(text, "//") {
		return nil, nil, 0, false
	}
	second, _ := utf8.DecodeRuneInString(text[1:])
	if unicode.IsSpace(second) {
		return nil, nil, 0, false
	}
	prefix = text[0]
	text = text[1:]
	for len(text) > 0 {
		first, width := utf8.DecodeRuneInString(text)
		if unicode.IsSpace(first) {
			text = text[width:]
			continue
		}
		i := strings.IndexFunc(text, unicode.IsSpace)
		if i < 0 {
			words = append(words, strings.ToLower(text))
			rests = append(rests, "")
			break
		}
		words = append(words, strings.ToLower(text[:i]))
		_, width = utf8.DecodeRuneInString(text[i:])
		rests = append(rests, text[i+width:])
		text = text[i+width:]
	}
	return words, rests, prefix, len(words) > 0
}

// Dispatch 在普通聊天前执行；识别到命令后即使失败也返回 handled=true。
func (r *Registry) Dispatch(ctx context.Context, message chat.Message, reply chat.Adapter, capabilities map[string]bool) (bool, error) {
	text := message.CommandText()
	words, rests, _, ok := Parse(text)
	if !ok {
		return false, nil
	}
	for length := min(len(words), r.maxDepth); length > 0; length-- {
		if index, found := r.paths[strings.Join(words[:length], " ")]; found {
			def := r.definitions[index]
			if def.Capability != "" && !capabilities[def.Capability] {
				return true, send(ctx, message, reply, "该平台暂不支持此命令")
			}
			if def.Authorize != nil && !def.Authorize(chat.Identity{Session: message.Session, SenderID: message.SenderID, Direct: message.Direct}) {
				return true, send(ctx, message, reply, "没有执行此命令的权限")
			}
			if def.Once {
				return true, r.dispatchOnce(ctx, message, reply, def, rests[length-1])
			}
			return true, def.Handle(ctx, message, reply, rests[length-1])
		}
	}
	return true, send(ctx, message, reply, "未知命令："+text)
}

func (r *Registry) dispatchOnce(ctx context.Context, message chat.Message, adapter chat.Adapter, def Definition, raw string) error {
	if r.receipts == nil {
		return send(ctx, message, adapter, "命令回执存储未启用，无法安全执行")
	}
	transaction := fmt.Sprintf("saber-command-%x", sha256.Sum256([]byte(string(message.Session.Key())+"\x00"+message.ID+"\x00"+def.ID)))
	state, body, err := r.receipts.ClaimCommand(ctx, message, def.ID)
	if err != nil {
		return err
	}
	switch state {
	case "done":
		_, err = adapter.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: body, TransactionID: transaction})
		return err
	case "new":
		tracked := &receiptAdapter{Adapter: adapter, transaction: transaction}
		if err = def.Handle(ctx, message, tracked, raw); err != nil {
			return err
		}
		if !tracked.sent {
			return errors.New("有副作用的命令未产生回执，需人工核查")
		}
		return r.receipts.CompleteCommand(ctx, message, def.ID, tracked.body)
	default:
		_, err = adapter.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: "该命令执行状态不确定，请核查后重试新消息；不会自动重复操作。", TransactionID: transaction})
		return err
	}
}

type receiptAdapter struct {
	chat.Adapter
	transaction string
	body        string
	sent        bool
}

func (a *receiptAdapter) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if reply.TransactionID == "" {
		reply.TransactionID = a.transaction
	}
	id, err := a.Adapter.Send(ctx, reply)
	if err == nil {
		a.body, a.sent = reply.Text, true
	}
	return id, err
}

func (a *receiptAdapter) SendImage(ctx context.Context, reply chat.Reply, data []byte, mimeType, filename string, width, height int) (string, error) {
	image, ok := a.Adapter.(chat.ImageAdapter)
	if !ok {
		return "", errors.New("该平台不支持图片发送")
	}
	if reply.TransactionID == "" {
		reply.TransactionID = a.transaction
	}
	id, err := image.SendImage(ctx, reply, data, mimeType, filename, width, height)
	if err == nil {
		a.body, a.sent = "图片已发送："+reply.Text, true
	}
	return id, err
}

func send(ctx context.Context, message chat.Message, reply chat.Adapter, body string) error {
	_, err := reply.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: body})
	return err
}
