package media

import (
	"regexp"
	"strings"
)

var whisperRepeat = regexp.MustCompile(`(?i)^(?:thank you|thanks|bye|you|ok|okay|the end|\.|\s|,|!)+$`)
var whisperSilencePhrases = map[string]struct{}{
	"thank you": {}, "thanks for watching": {}, "subscribe to my channel": {}, "like and subscribe": {},
	"please subscribe": {}, "thank you for watching": {}, "bye": {}, "you": {}, "the end": {},
	"продолжение следует": {}, "sous-titres": {}, "sous-titres réalisés par la communauté d'amara.org": {},
	"sottotitoli creati dalla comunità amara.org": {}, "untertitel von stephanie geiges": {},
	"amara.org": {}, "www.mooji.org": {}, "ご視聴ありがとうございました": {},
}

// voiceTranscript 只在连续语音模式过滤上游已知静音幻觉；显式停止短语始终保留。
func (s *Service) voiceTranscript(text string) string {
	if s.StopPhrase(text) {
		return text
	}
	cleaned := strings.ToLower(strings.TrimSpace(text))
	_, known := whisperSilencePhrases[strings.TrimRight(cleaned, ".!")]
	if cleaned == "" || known || whisperRepeat.MatchString(cleaned) {
		return ""
	}
	return text
}
