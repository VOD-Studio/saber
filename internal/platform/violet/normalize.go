package violetplatform

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"rua.plus/saber/internal/command"
)

// addressedCommand 只信任正文开头 token 中的完整用户 ID，参数中的提及不参与寻址。
func addressedCommand(content, botUserID string) (body string, commandLike, addressed bool) {
	if _, _, _, ok := command.Parse(content); ok {
		return content, true, false
	}
	indices := mentionTokenPattern.FindStringSubmatchIndex(content)
	if len(indices) < 6 || indices[0] != 0 {
		return "", false, false
	}
	rest := content[indices[1]:]
	gap, _ := utf8.DecodeRuneInString(rest)
	if rest == "" || !unicode.IsSpace(gap) {
		return "", false, false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if _, _, _, ok := command.Parse(rest); !ok {
		return "", false, false
	}
	return rest, true, content[indices[4]:indices[5]] == botUserID
}

// mentionTokenPattern 匹配 Violet 的提及占位符 @(username:uuid)。
//
// 用户名与 ID 里都不含冒号与右括号，因此这个非贪婪的字符类足够精确，
// 不会把正文里恰好相邻的括号一起吃掉。
var mentionTokenPattern = regexp.MustCompile(`@\(([^():]*):([^():]*)\)`)

// stripMentions 剥离提及 token 里的 ID 部分。
//
// Violet 把 @(name:uuid) 原样留在正文里，uuid 对模型是纯噪声，
// 而 @name 才是「在跟谁说话」的语义，因此降级保留而不是整段删掉。
func stripMentions(content string) string {
	return mentionTokenPattern.ReplaceAllString(content, "@$1")
}

// mentionsUser 判断正文是否点名了给定用户：用户名或用户 ID 任一命中即算。
//
// 用户名按大小写不敏感比较（Violet 的用户名唯一性不依赖大小写敏感），
// ID 精确匹配，两条路都留着是因为重命名与 token 形态变化都不该让被 @ 的消息消失。
func mentionsUser(content, username, userID string) bool {
	if username == "" && userID == "" {
		return false
	}
	for _, token := range mentionTokenPattern.FindAllStringSubmatch(content, -1) {
		name, id := token[1], token[2]
		if userID != "" && strings.EqualFold(id, userID) {
			return true
		}
		if username != "" && strings.EqualFold(name, username) {
			return true
		}
	}
	return false
}

// hasAnyMention 判断正文里是否存在任意提及，用于 kind 拿不准时的从严兜底。
func hasAnyMention(content string) bool {
	return mentionTokenPattern.MatchString(content)
}

// mentionResidue 返回抹掉全部提及 token 后剩下的正文。
//
// 只 @ 一下没说话的消息没有提问内容，回答它等于把「@saber」当成问题；
// 这个判断必须在剥离 token 之前用原文做，否则 @saber 会被当成有效正文。
func mentionResidue(content string) string {
	return strings.TrimSpace(mentionTokenPattern.ReplaceAllString(content, ""))
}

// truncateContent 把出站正文压进 Violet 的字符上限。
//
// 按 Unicode 字符而非字节计：中文正文按字节截断会切出半个字，
// 那不是「过长」而是乱码。第二个返回值表示是否发生截断。
func truncateContent(content string) (string, bool) {
	runes := []rune(content)
	if len(runes) <= maxContentRunes {
		return content, false
	}
	keep := maxContentRunes - len([]rune(contentTruncatedSuffix))
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + contentTruncatedSuffix, true
}

// parseTimestamp 解析 Violet 的 RFC3339 时间戳。解析失败时第二个返回值为 false，
// 调用方据此放弃水位判断，而不是把零值时间当起点。
func parseTimestamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
