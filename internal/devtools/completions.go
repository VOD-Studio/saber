package devtools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ReferenceCompletion 是已授权路径或已注册引用提供者返回的真实候选。
type ReferenceCompletion struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	Directory bool   `json:"directory,omitempty"`
	Size      int64  `json:"size,omitempty"`
}

// SetReferenceAutocomplete 绑定插件的真实补全协议，实际调用前仍检查当前注册表。
func (m *Manager) SetReferenceAutocomplete(callback func(context.Context, string, string, int) ([]string, error)) {
	m.mu.Lock()
	m.referenceAutocomplete = callback
	m.mu.Unlock()
}

// ContextCompletions 提供引用种类、Git 数目、授权目录路径及插件记录的交互式补全。
// 文件元数据来自执行器已有安全后端，管理器不直接打开宿主目录。
func (m *Manager) ContextCompletions(ctx context.Context, input string, max int) ([]ReferenceCompletion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !m.cfg.Enabled {
		return nil, errors.New("reference completion disabled")
	}
	if max <= 0 {
		max = 20
	}
	if max > 100 {
		max = 100
	}
	at := strings.LastIndexByte(input, '@')
	if at < 0 {
		return nil, errors.New("reference completion requires @")
	}
	tail := input[at+1:]
	m.mu.Lock()
	factory, customComplete := m.referenceProviders, m.referenceAutocomplete
	m.mu.Unlock()
	var custom map[string]func(context.Context, string, int) (string, error)
	if factory != nil {
		custom = factory(ctx)
	}
	kind, target, hasTarget := strings.Cut(tail, ":")
	if !hasTarget {
		names := []string{"diff", "staged", "file:", "folder:", "git:", "url:"}
		for prefix := range custom {
			names = append(names, prefix+":")
		}
		sort.Strings(names)
		var out []ReferenceCompletion
		for _, name := range names {
			if strings.HasPrefix(name, kind) {
				out = append(out, ReferenceCompletion{Value: "@" + name, Label: "@" + name})
				if len(out) == max {
					break
				}
			}
		}
		return out, nil
	}
	if len(target) > 0 && (target[0] == '\'' || target[0] == '"' || target[0] == '`') {
		quote := target[0]
		target = target[1:]
		if len(target) > 0 && target[len(target)-1] == quote {
			target = target[:len(target)-1]
		}
	}
	var candidates []ReferenceCompletion
	switch kind {
	case "file", "folder":
		backend, ok := m.backend.(interface {
			DeveloperComplete(context.Context, string, string, int) ([]ReferenceCompletion, error)
		})
		if !ok {
			return nil, errors.New("authorized filesystem completion backend unavailable")
		}
		var err error
		candidates, err = backend.DeveloperComplete(ctx, kind, target, max)
		if err != nil {
			return nil, err
		}
	case "git":
		for count := range 10 {
			value := strconv.Itoa(count + 1)
			if strings.HasPrefix(value, target) {
				candidates = append(candidates, ReferenceCompletion{Value: value, Label: fmt.Sprintf("Last %d commits with patches", count+1)})
			}
		}
	case "url":
		return []ReferenceCompletion{}, nil
	case "diff", "staged":
		return nil, errors.New("this reference takes no target")
	default:
		if custom[kind] == nil {
			return nil, errors.New("context reference provider is not currently registered")
		}
		if customComplete == nil {
			return nil, errors.New("registered provider has no autocomplete protocol")
		}
		values, err := customComplete(ctx, kind, target, max)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			candidates = append(candidates, ReferenceCompletion{Value: value, Label: value})
		}
	}
	out := make([]ReferenceCompletion, 0, min(len(candidates), max))
	bytes := 0
	for _, candidate := range candidates {
		if len(out) == max {
			break
		}
		if candidate.Directory && !strings.HasSuffix(candidate.Value, "/") {
			candidate.Value += "/"
		}
		value, ok := completionValue(candidate.Value)
		if !ok {
			continue
		}
		candidate.Value = "@" + kind + ":" + value
		bytes += len(candidate.Value) + len(candidate.Label)
		if bytes > m.cfg.ReferenceBytes {
			return nil, errors.New("reference completion exceeds configured byte limit")
		}
		out = append(out, candidate)
	}
	return out, nil
}
func completionValue(value string) (string, bool) {
	if strings.ContainsAny(value, "\n\r\x00") {
		return "", false
	}
	quote := strings.HasSuffix(value, ",") || strings.HasSuffix(value, ".") || strings.HasSuffix(value, ";") || strings.HasSuffix(value, "!") || strings.HasSuffix(value, "?")
	for _, r := range value {
		if unicode.IsSpace(r) || strings.ContainsRune("()[]{}\"'`", r) {
			quote = true
			break
		}
	}
	if !quote {
		return value, true
	}
	for _, mark := range []byte{'"', '\'', '`'} {
		if !strings.ContainsRune(value, rune(mark)) {
			return string(mark) + value + string(mark), true
		}
	}
	return "", false
}
