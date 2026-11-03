package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
)

const (
	// maxReferenceFiles 限制单条消息展开的 @ 引用数量。
	maxReferenceFiles = 8
	// maxReferenceTotalBytes 限制单条消息全部引用正文的合计大小。
	maxReferenceTotalBytes = 64 << 10
)

// referenceToken 是消息中解析出的一个 @ 路径引用。
type referenceToken struct {
	// Path 是去掉引号后的路径文本。
	Path string
}

// referenceTokens 解析消息中的 @path 与 @"path with space" 引用。
//
// 只在行首、空白或左括号之后识别 @，避免把邮箱地址当成引用；未闭合的引号
// 与空路径保持原文不变。
func referenceTokens(text string) []referenceToken {
	var tokens []referenceToken
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		if i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:i])
			switch prev {
			case ' ', '\t', '\n', '\r', '(', '（':
			default:
				continue
			}
		}
		if i+1 < len(text) && text[i+1] == '"' {
			rest := text[i+2:]
			end := strings.IndexByte(rest, '"')
			if end < 0 {
				continue
			}
			path := rest[:end]
			if path == "" || strings.ContainsAny(path, "\r\n") {
				continue
			}
			tokens = append(tokens, referenceToken{Path: path})
			i += 2 + end
			continue
		}
		j := i + 1
		for j < len(text) && !isReferenceSpace(text[j]) {
			j++
		}
		raw := text[i+1 : j]
		if cut := strings.IndexAny(raw, "，。；：！？、）"); cut >= 0 {
			raw = raw[:cut]
		}
		path := strings.TrimRight(raw, ".,;:!?，。；：！？、)）")
		if path == "" {
			continue
		}
		tokens = append(tokens, referenceToken{Path: path})
		i = j - 1
	}
	return tokens
}

// isReferenceSpace 判断 @ 路径的分隔字符。
func isReferenceSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}

// expandReferences 把消息中的 @ 引用展开为文件内容并追加到原文本之后。
//
// 只在可信身份持有一个已授权工作区时生效；原消息文本保持不变，无法解析的
// 路径按跳过说明追加，不存在或越界的引用不改变文本。
func (s *Service) expandReferences(ctx context.Context, message chat.Message) chat.Message {
	if s.executor == nil || message.Text == "" {
		return message
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return message
	}
	if _, err := s.executor.Workspace(identity); err != nil {
		return message
	}
	tokens := referenceTokens(message.Text)
	if len(tokens) == 0 {
		return message
	}
	var blocks, notes []string
	total, added := 0, 0
	for _, token := range tokens {
		if added >= maxReferenceFiles {
			notes = append(notes, fmt.Sprintf("[跳过引用 @%s：最多引用 %d 个文件]", token.Path, maxReferenceFiles))
			break
		}
		reference, err := s.executor.ReadReference(ctx, token.Path)
		if errors.Is(err, execution.ErrReferenceNotFound) {
			continue
		}
		if err != nil {
			notes = append(notes, fmt.Sprintf("[跳过引用 @%s：%s]", token.Path, err.Error()))
			continue
		}
		if total+len(reference.Content) > maxReferenceTotalBytes {
			notes = append(notes, fmt.Sprintf("[跳过引用 @%s：超出合计 %d KiB 限制]", token.Path, maxReferenceTotalBytes>>10))
			continue
		}
		added++
		total += len(reference.Content)
		blocks = append(blocks, referenceBlock(reference.Path, reference.Content))
	}
	if len(blocks) == 0 && len(notes) == 0 {
		return message
	}
	var b strings.Builder
	b.WriteString(message.Text)
	if len(blocks) > 0 {
		b.WriteString("\n\n以下为消息中 @ 引用的文件内容：\n")
		b.WriteString(strings.Join(blocks, "\n"))
	}
	for _, note := range notes {
		b.WriteString("\n")
		b.WriteString(note)
	}
	message.Text = b.String()
	return message
}
