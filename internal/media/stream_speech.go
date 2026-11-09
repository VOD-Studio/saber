package media

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

// SpeechStream 在模型正文增量到达时逐句合成，保留跨块的思考/代码标记状态；取消立即停止。
func (s *Service) SpeechStream(ctx context.Context, deltas <-chan string, emit func(chat.Attachment) error) error {
	if deltas == nil || emit == nil {
		return errors.New("speech delta source and consumer required")
	}
	var text strings.Builder
	var markup strings.Builder
	var pending string
	inThink, inCode, inTag := false, false, false
	flush := func(force bool) error {
		clean := NormalizeSpeech(text.String())
		if clean == "" {
			text.Reset()
			return nil
		}
		if !force && len(clean) < 20 {
			return nil
		}
		a, err := s.Synthesize(ctx, clean)
		if err != nil {
			return err
		}
		if err = emit(a); err != nil {
			return err
		}
		text.Reset()
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delta, ok := <-deltas:
			if !ok {
				if !inThink && !inCode && !inTag {
					if pending != "" {
						text.WriteString(pending)
					}
					return flush(true)
				}
				return flush(true)
			}
			pending += delta
			for len(pending) > 0 {
				// 标记可能横跨模型 token，保留其前缀到下一块，避免朗读隐藏推理。
				if pending == "`" || pending == "``" || pending == "<" || pending == "<t" || pending == "<th" || pending == "<thi" || pending == "<thin" || pending == "<think" || pending == "</" || pending == "</t" || pending == "</th" || pending == "</thi" || pending == "</thin" || pending == "</think" {
					break
				}
				if strings.HasPrefix(pending, "<think>") {
					inThink = true
					pending = pending[len("<think>"):]
					continue
				}
				if strings.HasPrefix(pending, "</think>") {
					inThink = false
					pending = pending[len("</think>"):]
					continue
				}
				if strings.HasPrefix(pending, "```") {
					if !inThink {
						inCode = !inCode
					}
					pending = pending[3:]
					continue
				}
				r, n := utf8.DecodeRuneInString(pending)
				if r == utf8.RuneError && n == 1 && !utf8.FullRuneInString(pending) {
					break
				}
				pending = pending[n:]
				if inThink || inCode {
					continue
				}
				if r == '<' {
					inTag = true
					markup.Reset()
					continue
				}
				if inTag {
					if r == '>' {
						inTag = false
						markup.Reset()
					} else {
						markup.WriteRune(r)
						if markup.Len() > 4096 {
							return errors.New("speech markup exceeds limit")
						}
					}
					continue
				}
				text.WriteRune(r)
				if text.Len() > 8000 {
					if err := flush(true); err != nil {
						return err
					}
				} else if strings.ContainsRune(".!?。！？\n", r) {
					if err := flush(false); err != nil {
						return err
					}
				}
			}
		}
	}
}
