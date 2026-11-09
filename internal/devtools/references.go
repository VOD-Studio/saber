package devtools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ContextReference 保存内联引用、原文位置及可选的一基闭区间行号。
type ContextReference struct {
	Raw       string `json:"raw"`
	Kind      string `json:"kind"`
	Target    string `json:"target,omitempty"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

// ReferenceSource 的所有操作必须走现有执行器授权/后端及 Web URL 安全检查。
// 回调不得因为引用而另开宿主路径或把任务目录之外的敏感文件返回给模型。
type ReferenceSource struct {
	Read   func(context.Context, string, int, int, int) (string, error)
	Folder func(context.Context, string, int, int) (string, error)
	Git    func(context.Context, []string, int) (string, error)
	URL    func(context.Context, string, int) (string, error)
	Custom map[string]func(context.Context, string, int) (string, error)
}

// ReferenceResult 记录已扩展正文和大小拒绝；拒绝时 Message 始终为未附加内容的原文。
type ReferenceResult struct {
	Message         string             `json:"message"`
	References      []ContextReference `json:"references"`
	Warnings        []string           `json:"warnings,omitempty"`
	EstimatedTokens int                `json:"estimated_tokens"`
	Blocked         bool               `json:"blocked"`
}

var referenceHead = regexp.MustCompile(`@(diff|staged)\b|@(file|folder|git|url):`)
var customReferenceHead = regexp.MustCompile(`@(diff|staged)\b|@([A-Za-z][A-Za-z0-9_-]*):`)
var lineRange = regexp.MustCompile(`:(\d+)(?:-(\d+))?$`)

// ParseReferences 支持引号中的空格路径、行范围、末尾标点及 URL 成对括号，跳过邮箱内的 @。
func ParseReferences(message string) []ContextReference { return parseReferences(message, nil) }
func parseReferences(message string, custom map[string]func(context.Context, string, int) (string, error)) []ContextReference {
	var references []ContextReference
	cursor := 0
	head := referenceHead
	if len(custom) > 0 {
		head = customReferenceHead
	}
	for cursor < len(message) {
		match := head.FindStringSubmatchIndex(message[cursor:])
		if match == nil {
			break
		}
		start := cursor + match[0]
		headEnd := cursor + match[1]
		cursor = headEnd
		if start > 0 {
			r, _ := utf8.DecodeLastRuneInString(message[:start])
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '/' {
				continue
			}
		}
		kind := ""
		target := ""
		end := headEnd
		if match[2] >= 0 {
			kind = message[cursor-match[1]+match[2] : cursor-match[1]+match[3]]
		} else {
			kind = message[cursor-match[1]+match[4] : cursor-match[1]+match[5]]
			if headEnd >= len(message) {
				continue
			}
			quote := message[headEnd]
			if quote == '\'' || quote == '"' || quote == '`' {
				stop := strings.IndexByte(message[headEnd+1:], quote)
				if stop < 0 {
					continue
				}
				end = headEnd + 1 + stop + 1
				target = message[headEnd+1 : end-1]
				if kind == "file" {
					suffix := end
					for end < len(message) && (message[end] == ':' || message[end] == '-' || message[end] >= '0' && message[end] <= '9') {
						end++
					}
					target += message[suffix:end]
				}
			} else {
				for end < len(message) {
					r, size := utf8.DecodeRuneInString(message[end:])
					if unicode.IsSpace(r) {
						break
					}
					end += size
				}
				target = stripReferencePunctuation(message[headEnd:end])
				end = headEnd + len(target)
			}
		}
		if kind != "diff" && kind != "staged" && target == "" {
			continue
		}
		if kind != "file" && kind != "folder" && kind != "git" && kind != "url" && kind != "diff" && kind != "staged" && custom[kind] == nil {
			continue
		}
		ref := ContextReference{Raw: message[start:end], Kind: kind, Target: target, Start: start, End: end}
		if kind == "file" {
			if loc := lineRange.FindStringSubmatchIndex(target); loc != nil {
				first, _ := strconv.Atoi(target[loc[2]:loc[3]])
				last := first
				if loc[4] >= 0 {
					last, _ = strconv.Atoi(target[loc[4]:loc[5]])
				}
				ref.Target = target[:loc[0]]
				if first >= 1 && last >= first {
					ref.LineStart, ref.LineEnd = first, last
				}
			}
		}
		references = append(references, ref)
		cursor = end
	}
	return references
}
func stripReferencePunctuation(value string) string {
	value = strings.TrimRight(value, ",.;!?")
	for len(value) > 0 {
		last := value[len(value)-1]
		var opener byte
		switch last {
		case ')':
			opener = '('
		case ']':
			opener = '['
		case '}':
			opener = '{'
		default:
			return value
		}
		if strings.Count(value, string(last)) <= strings.Count(value, string(opener)) {
			return value
		}
		value = value[:len(value)-1]
	}
	return value
}

// GitReferenceArgs 返回固定只读 Git 参数；模型值只影响有界提交数，不变成任意命令。
func GitReferenceArgs(reference ContextReference) ([]string, error) {
	switch reference.Kind {
	case "diff":
		return []string{"diff", "--no-ext-diff", "--no-textconv", "--"}, nil
	case "staged":
		return []string{"diff", "--cached", "--no-ext-diff", "--no-textconv", "--"}, nil
	case "git":
		count, err := strconv.Atoi(reference.Target)
		if err != nil {
			return nil, errors.New("git reference requires a commit count")
		}
		if count < 1 {
			count = 1
		}
		if count > 10 {
			count = 10
		}
		return []string{"log", "-p", "--no-ext-diff", "--no-textconv", "--max-count=" + strconv.Itoa(count), "--"}, nil
	default:
		return nil, errors.New("not a Git context reference")
	}
}

// SetReferenceProviders 在每次扩展时取得插件的当前注册表，停用插件不留下旧的可调用能力。
func (m *Manager) SetReferenceProviders(factory func(context.Context) map[string]func(context.Context, string, int) (string, error)) {
	m.mu.Lock()
	m.referenceProviders = factory
	m.mu.Unlock()
}

// ExpandReferences 附加完整文件/目录/Git/URL 上下文，并统一执行每引用及整个上下文的限额。
// 安全拒绝保留具体原因，取消立即返回错误，绝不在缺少回调时自行读取或联网。
func (m *Manager) ExpandReferences(ctx context.Context, message string, source ReferenceSource, contextTokens int) (ReferenceResult, error) {
	if err := ctx.Err(); err != nil {
		return ReferenceResult{Message: message}, err
	}
	if !m.cfg.Enabled {
		return ReferenceResult{Message: message, References: ParseReferences(message)}, nil
	}
	if source.Custom == nil {
		m.mu.Lock()
		factory := m.referenceProviders
		m.mu.Unlock()
		if factory != nil {
			source.Custom = factory(ctx)
		}
	}
	result := ReferenceResult{Message: message, References: parseReferences(message, source.Custom)}
	if len(result.References) == 0 {
		return result, nil
	}
	var attached strings.Builder
	for i, reference := range result.References {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if i >= m.cfg.MaxReferences {
			result.Warnings = append(result.Warnings, fmt.Sprintf("only %d references can be expanded in one message", m.cfg.MaxReferences))
			break
		}
		content, err := m.expandReference(ctx, reference, source)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.Warnings = append(result.Warnings, reference.Raw+": "+err.Error())
			continue
		}
		if len(content) > m.cfg.ReferenceBytes {
			result.Warnings = append(result.Warnings, reference.Raw+": content exceeds reference byte limit; select a narrower file range or Git diff")
			continue
		}
		if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
			result.Warnings = append(result.Warnings, reference.Raw+": reference is not UTF-8 text")
			continue
		}
		if strings.TrimSpace(content) == "" {
			result.Warnings = append(result.Warnings, reference.Raw+": no content extracted")
			continue
		}
		fence := contextFence(content)
		fmt.Fprintf(&attached, "\n%s\n%s\n%s\n%s\n", reference.Raw, fence, content, fence)
		if attached.Len() > m.cfg.ReferenceBytes {
			result.Blocked = true
			result.Warnings = append(result.Warnings, "attached context exceeds the total reference byte limit; no references were attached")
			return result, nil
		}
	}
	result.EstimatedTokens = (utf8.RuneCountInString(message) + utf8.RuneCountInString(attached.String()) + 2) / 3
	if attached.Len() > 0 && contextTokens > 0 && result.EstimatedTokens > contextTokens/2 {
		result.Blocked = true
		result.Warnings = append(result.Warnings, "references exceed 50% of context capacity; no references were attached")
		return result, nil
	}
	if attached.Len() > 0 && contextTokens > 0 && result.EstimatedTokens > contextTokens/4 {
		result.Warnings = append(result.Warnings, "references exceed 25% of context capacity (rough token estimate)")
	}
	if attached.Len() > 0 {
		result.Message = message + "\n\n--- Attached Context ---\n" + attached.String()
	}
	if len(result.Warnings) > 0 {
		result.Message += "\n\n--- Context Reference Warnings ---\n" + strings.Join(result.Warnings, "\n")
	}
	return result, nil
}
func contextFence(text string) string {
	maximum, current := 2, 0
	for _, c := range text {
		if c == '`' {
			current++
			if current > maximum {
				maximum = current
			}
		} else {
			current = 0
		}
	}
	return strings.Repeat("`", maximum+1)
}
func (m *Manager) expandReference(ctx context.Context, ref ContextReference, source ReferenceSource) (string, error) {
	switch ref.Kind {
	case "file":
		if source.Read == nil {
			return "", errors.New("authorized file reference reader unavailable")
		}
		return source.Read(ctx, ref.Target, ref.LineStart, ref.LineEnd, m.cfg.ReferenceBytes)
	case "folder":
		if source.Folder == nil {
			return "", errors.New("authorized folder reference reader unavailable")
		}
		return source.Folder(ctx, ref.Target, m.cfg.FolderEntries, m.cfg.ReferenceBytes)
	case "diff", "staged", "git":
		if source.Git == nil {
			return "", errors.New("authorized Git reference backend unavailable")
		}
		args, err := GitReferenceArgs(ref)
		if err != nil {
			return "", err
		}
		return source.Git(ctx, args, m.cfg.ReferenceBytes)
	case "url":
		if source.URL == nil {
			return "", errors.New("authorized URL reference fetcher unavailable")
		}
		parsed, err := url.Parse(ref.Target)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", errors.New("URL reference requires HTTP(S) without credentials")
		}
		return source.URL(ctx, ref.Target, m.cfg.ReferenceBytes)
	default:
		if provider := source.Custom[ref.Kind]; provider != nil {
			return provider(ctx, ref.Target, m.cfg.ReferenceBytes)
		}
		return "", errors.New("unsupported reference kind")
	}
}
