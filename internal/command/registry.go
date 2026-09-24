// Package command 维护聊天命令的解析、执行与平台目录。
package command

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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
	Handle     Handler
}

// Registry 是启动期间装配的命令集合；启动后只读。
type Registry struct {
	definitions []Definition
	paths       map[string]int
}

// New 创建空注册表。
func New() *Registry { return &Registry{paths: make(map[string]int)} }

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
	}
	r.definitions = append(r.definitions, def)
	return nil
}

// Describe 返回该平台可执行的目录快照。
func (r *Registry) Describe(capabilities map[string]bool) []Descriptor {
	result := make([]Descriptor, 0, len(r.definitions))
	for _, def := range r.definitions {
		if def.Capability == "" || capabilities[def.Capability] {
			result = append(result, def.Descriptor)
		}
	}
	slices.SortFunc(result, func(a, b Descriptor) int {
		return strings.Compare(strings.Join(a.Path, " "), strings.Join(b.Path, " "))
	})
	return result
}

// Parse 只识别正文开头的命令；转义的 // 和非命令正文均返回 ok=false。
// rests[i] 保留第 i 个词后跳过一个分隔符的原文，供处理器读取未改写参数。
func Parse(text string) (words, rests []string, prefix byte, ok bool) {
	if len(text) < 2 || text[0] != '/' && text[0] != '!' || strings.HasPrefix(text, "//") {
		return nil, nil, 0, false
	}
	prefix = text[0]
	text = text[1:]
	for len(text) > 0 {
		if strings.ContainsRune(" \t\r\n", rune(text[0])) {
			text = text[1:]
			continue
		}
		i := strings.IndexAny(text, " \t\r\n")
		if i < 0 {
			words = append(words, strings.ToLower(text))
			rests = append(rests, "")
			break
		}
		words = append(words, strings.ToLower(text[:i]))
		rests = append(rests, text[i+1:])
		text = text[i+1:]
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
	for length := len(words); length > 0; length-- {
		if index, found := r.paths[strings.Join(words[:length], " ")]; found {
			def := r.definitions[index]
			if def.Capability != "" && !capabilities[def.Capability] {
				return true, send(ctx, message, reply, "该平台暂不支持此命令")
			}
			if def.Authorize != nil && !def.Authorize(chat.Identity{Session: message.Session, SenderID: message.SenderID}) {
				return true, send(ctx, message, reply, "没有执行此命令的权限")
			}
			return true, def.Handle(ctx, message, reply, rests[length-1])
		}
	}
	return true, send(ctx, message, reply, "未知命令："+text)
}

func send(ctx context.Context, message chat.Message, reply chat.Adapter, body string) error {
	_, err := reply.Send(ctx, chat.Reply{Session: message.Session, ReplyTo: message.ID, Text: body})
	return err
}
