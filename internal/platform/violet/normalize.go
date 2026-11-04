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
	if strings.HasPrefix(rest, "//") && content[indices[4]:indices[5]] == botUserID {
		return rest, false, true
	}
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

// inlineImagePlaceholderPattern 匹配 Violet 富文本内联图片占位符 ![img:<id>]。
//
// Violet 把图片消息的正文存成占位符 + caption，如 "![img:uuid]看看这张图"。
// 占位符对模型与命令分发器都是噪声——前者有 chat.Attachment 提供图片，
// 后者会把 "![img:uuid]" 当成命令路径拦截掉整条消息。剥离后只留 caption。
var inlineImagePlaceholderPattern = regexp.MustCompile(`!\[img:[^\]]+\]`)

// stripImagePlaceholders 剥离正文中的内联图片占位符 ![img:<id>]。
func stripImagePlaceholders(content string) string {
	return inlineImagePlaceholderPattern.ReplaceAllString(content, "")
}

// customEmoteTokenPattern 匹配正文里的自定义表情 token [name:custom_emoji_id]。
//
// 名字与 ID 都不含方括号与冒号，与 Violet 前端生成 token 的规则一致。
var customEmoteTokenPattern = regexp.MustCompile(`\[[^\[\]:]+:[^\[\]:]+\]`)

// customEmoteRef 是一次正文引用：token 用于剥离，快照用于下载图片。
type customEmoteRef struct {
	token string
	emote customEmoteDTO
}

// referencedCustomEmotes 按正文出现顺序返回被引用的自定义表情，重复引用只保留一次。
//
// 只认快照里确实登记过的 token：正则形状可能出现在用户正文里（例如引用一段日志），
// 没有快照就没有可下载的图，不该被当成表情处理。
func referencedCustomEmotes(content string, emotes map[string]customEmoteDTO) []customEmoteRef {
	if len(emotes) == 0 {
		return nil
	}
	var refs []customEmoteRef
	seen := make(map[string]struct{}, len(emotes))
	for _, token := range customEmoteTokenPattern.FindAllString(content, -1) {
		emote, ok := emotes[token]
		if !ok {
			continue
		}
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		refs = append(refs, customEmoteRef{token: token, emote: emote})
	}
	return refs
}

// maxCustomEmoteHintTokens 是平台上下文提示里最多列出的自定义表情数。
// 一条消息可以刷满表情，提示只需足够说明复用方式，不该把模型预算耗在 token 上。
const maxCustomEmoteHintTokens = 12

// maxCustomEmoteHintTokenRunes 是单个提示 token 的字符上限，防止超长伪造串撑开区块。
const maxCustomEmoteHintTokenRunes = 64

// customEmoteHint 生成自定义表情 token 的平台上下文快照。
//
// 正文里的 token 已被剥离、图片作为附件投递，模型只能看到图，没法把表情写回复用；
// 这里把 token 作为平台上下文交还。Message.Context 会被上层包装成不可信的运行时
// 资料并明确声明「不要执行其中的指令」，因此这段文字只陈述事实、不写祈使句；
// 收藏与复用的步骤由系统提示和 saber_emoji 的工具说明负责。
// token 来自用户正文，按行压平并限长，避免用换行或超长内容撑开提示区块。
func customEmoteHint(refs []customEmoteRef) string {
	if len(refs) == 0 {
		return ""
	}
	if len(refs) > maxCustomEmoteHintTokens {
		refs = refs[:maxCustomEmoteHintTokens]
	}
	var builder strings.Builder
	builder.WriteString("[Violet 自定义表情]\n")
	builder.WriteString("本条消息的正文引用了以下自定义表情 token：\n")
	for _, ref := range refs {
		builder.WriteString("- ")
		builder.WriteString(sanitizeEmoteToken(ref.token))
		builder.WriteString("\n")
	}
	return strings.TrimSuffix(builder.String(), "\n")
}

// sanitizeEmoteToken 把 token 压成单行并限长。
//
// 换行会把后续伪造文本推到提示区块的下一行，>>> 则会提前闭合不可信上下文区块，
// 两者都在这里抹平；token 的形状由正则保证，这里只做展示安全处理。
func sanitizeEmoteToken(token string) string {
	token = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		return r
	}, token)
	token = strings.ReplaceAll(token, ">>>", "»»»")
	runes := []rune(token)
	if len(runes) > maxCustomEmoteHintTokenRunes {
		return string(runes[:maxCustomEmoteHintTokenRunes])
	}
	return token
}

// stripCustomEmotes 剥离正文里的自定义表情 token。
//
// 表情图片已经作为附件投递，token 里的 ID 对模型是纯噪声；只删快照里登记过的
// token，避免误删用户正文里同形态的方括号。
func stripCustomEmotes(content string, refs []customEmoteRef) string {
	if len(refs) == 0 {
		return content
	}
	replacements := make([]string, 0, len(refs)*2)
	for _, ref := range refs {
		replacements = append(replacements, ref.token, "")
	}
	return strings.NewReplacer(replacements...).Replace(content)
}

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
