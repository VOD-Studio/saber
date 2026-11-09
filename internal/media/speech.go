package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"rua.plus/saber/internal/chat"
)

// Transcript 保留实际识别文本及供应商时间戳，不把元数据冒充语音内容。
type Transcript struct {
	Text     string `json:"text"`
	Provider string `json:"provider"`
	Language string `json:"language,omitempty"`
	Segments []any  `json:"segments,omitempty"`
}

// Transcribe 将音频上传给显式选择的 ASR 或授权本地命令；取消和权限拒绝不降级吞错。
func (s *Service) Transcribe(ctx context.Context, a chat.Attachment) (Transcript, error) {
	p := s.cfg.ASR
	var result Transcript
	if err := s.authorize(ctx, "audio_transcribe", p); err != nil {
		return result, err
	}
	ctx, done := s.scope(ctx)
	defer done()
	if a.Kind != "audio" && a.Kind != "video" {
		return result, errors.New("ASR requires audio/video attachment")
	}
	data, mt, err := s.load(ctx, a)
	if err != nil {
		return result, err
	}
	if len(data) == 0 {
		return result, errors.New("ASR audio empty")
	}
	p, err = s.speechProvider(ctx, p, "stt")
	if err != nil {
		return result, err
	}
	var response map[string]any
	switch p.Provider {
	case "whisper_cpp":
		response, err = s.localTranscribe(ctx, p, data, mt)
	case "command", "plugin":
		response, err = s.command(ctx, p, map[string]any{"operation": "transcribe", "audio": base64.StdEncoding.EncodeToString(data), "mime_type": mt, "model": p.Model, "language": p.Language, "options": p.Options})
	default:
		base, _, e := providerRuntime(p)
		if e != nil {
			return result, e
		}
		path := "/audio/transcriptions"
		field := "model"
		fallback := "whisper-1"
		switch p.Provider {
		case "groq":
			fallback = "whisper-large-v3-turbo"
		case "mistral":
			fallback = "voxtral-mini-latest"
		case "xai":
			path = "/stt"
			fallback = "grok-voice-transcribe-2.0"
		case "elevenlabs":
			path = "/speech-to-text"
			field = "model_id"
			fallback = "scribe_v2"
		}
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		fields := map[string]string{field: model(p, fallback)}
		if p.Language != "" {
			key := "language"
			if p.Provider == "elevenlabs" {
				key = "language_code"
			}
			fields[key] = p.Language
		}
		for k, v := range p.Options {
			if k != "model" && k != "model_id" && k != "file" {
				fields[k] = fmt.Sprint(v)
			}
		}
		for k, v := range fields {
			if e = w.WriteField(k, v); e != nil {
				return result, e
			}
		}
		filename := "audio"
		if ext, e := mime.ExtensionsByType(strings.Split(mt, ";")[0]); e == nil && len(ext) > 0 {
			filename += ext[0]
		} else {
			filename += ".wav"
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
		if mt == "" {
			mt = "application/octet-stream"
		}
		h.Set("Content-Type", mt)
		part, e := w.CreatePart(h)
		if e != nil {
			return result, e
		}
		if _, e = part.Write(data); e != nil {
			return result, e
		}
		if e = w.Close(); e != nil {
			return result, e
		}
		raw, _, e := s.request(ctx, p, "POST", base+path, w.FormDataContentType(), &b)
		if e != nil {
			return result, e
		}
		err = json.Unmarshal(raw, &response)
	}
	if err != nil {
		return result, err
	}
	if v := response["error"]; v != nil {
		return result, fmt.Errorf("ASR provider: %v", v)
	}
	text, ok := response["text"].(string)
	if !ok {
		text, ok = response["transcript"].(string)
	}
	if !ok {
		return result, errors.New("ASR provider did not return transcript text")
	}
	return Transcript{Text: strings.TrimSpace(text), Provider: p.Provider, Language: str(response, "language"), Segments: list(response, "segments")}, nil
}

// Synthesize 生成真实音频附件；请求模型、声线和凭据只来自可信配置。
func (s *Service) Synthesize(ctx context.Context, text string) (chat.Attachment, error) {
	p := s.cfg.TTS
	var empty chat.Attachment
	if err := s.authorize(ctx, "tts", p); err != nil {
		return empty, err
	}
	ctx, done := s.scope(ctx)
	defer done()
	text = NormalizeSpeech(text)
	if text == "" {
		return empty, errors.New("TTS text empty")
	}
	if len(text) > 64000 {
		return empty, errors.New("TTS text exceeds 64000 bytes; use sentence streaming")
	}
	format := p.Format
	if format == "" {
		format = "mp3"
	}
	voice := p.Voice
	if voice == "" {
		switch p.Provider {
		case "elevenlabs":
			voice = "pNInz6obpgDQGcFmaJgB"
		case "minimax":
			voice = "English_expressive_narrator"
		case "mistral":
			voice = "c69964a6-ab8b-4f8a-9465-ec0925096ec8"
		case "deepinfra":
			voice = "default"
		default:
			voice = "alloy"
		}
	}
	if p.Provider == "command" || p.Provider == "plugin" {
		r, e := s.command(ctx, p, map[string]any{"operation": "synthesize", "text": text, "model": p.Model, "voice": voice, "format": format, "options": p.Options})
		if e != nil {
			return empty, e
		}
		return s.decodeAudio(ctx, r)
	}
	if p.Provider == "edge" {
		return s.edgeSpeech(ctx, p, text)
	}
	if p.Provider == "piper" || p.Provider == "espeak" {
		return s.localSpeech(ctx, p, text)
	}
	base, _, err := providerRuntime(p)
	if err != nil {
		return empty, err
	}
	p, err = s.speechProvider(ctx, p, "tts")
	if err != nil {
		return empty, err
	}
	payload := options(p)
	path := "/audio/speech"
	payload["model"] = model(p, "gpt-4o-mini-tts")
	payload["input"] = text
	payload["voice"] = voice
	payload["response_format"] = format
	if p.Speed > 0 {
		payload["speed"] = p.Speed
	}
	switch p.Provider {
	case "elevenlabs":
		path = "/text-to-speech/" + url.PathEscape(voice) + "/stream"
		payload = options(p)
		payload["text"] = text
		payload["model_id"] = model(p, "eleven_multilingual_v2")
		if format == "opus" {
			path += "?output_format=opus_48000_64"
		} else {
			path += "?output_format=mp3_44100_128"
			format = "mp3"
		}
	case "xai":
		path = "/tts"
		payload = options(p)
		payload["text"] = text
		if p.Voice == "" {
			voice = "eve"
		}
		payload["voice_id"] = voice
		language := p.Language
		if language == "" {
			language = "en"
		}
		payload["language"] = language
		payload["output_format"] = map[string]any{"codec": format}
		if p.Speed > 0 {
			payload["speed"] = p.Speed
		}
	case "minimax":
		path = "/t2a_v2"
		payload = options(p)
		payload["model"] = model(p, "speech-02-hd")
		payload["text"] = text
		payload["stream"] = false
		speed := p.Speed
		if speed == 0 {
			speed = 1
		}
		payload["voice_setting"] = map[string]any{"voice_id": voice, "speed": speed}
		payload["audio_setting"] = map[string]any{"sample_rate": 32000, "format": format, "channel": 1}
	case "mistral":
		delete(payload, "voice")
		payload["voice_id"] = voice
		payload["model"] = model(p, "voxtral-mini-tts-2603")
	case "gemini":
		path = "/models/" + url.PathEscape(model(p, "gemini-2.5-flash-preview-tts")) + ":generateContent"
		if p.Voice == "" {
			voice = "Kore"
		}
		payload = map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": text}}}}, "generationConfig": map[string]any{"responseModalities": []string{"AUDIO"}, "speechConfig": map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": voice}}}}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return empty, err
	}
	raw, mt, err := s.request(ctx, p, "POST", base+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return empty, err
	}
	if p.Provider == "minimax" || p.Provider == "mistral" || p.Provider == "gemini" {
		var r map[string]any
		if err = json.Unmarshal(raw, &r); err != nil {
			return empty, fmt.Errorf("TTS invalid JSON: %w", err)
		}
		switch p.Provider {
		case "minimax":
			status := object(r, "base_resp")
			if code, ok := status["status_code"].(float64); !ok || code != 0 {
				return empty, fmt.Errorf("MiniMax TTS error: %v", status)
			}
			raw, err = hex.DecodeString(str(object(r, "data"), "audio"))
		case "mistral":
			raw, err = base64.StdEncoding.DecodeString(str(r, "audio_data"))
		case "gemini":
			candidates := list(r, "candidates")
			if len(candidates) == 0 {
				return empty, errors.New("gemini TTS returned no candidates")
			}
			candidate, _ := candidates[0].(map[string]any)
			parts := list(object(candidate, "content"), "parts")
			found := false
			for _, v := range parts {
				part, _ := v.(map[string]any)
				inline := object(part, "inlineData")
				if inline != nil {
					raw, err = base64.StdEncoding.DecodeString(str(inline, "data"))
					raw = pcmWAV(raw, 24000)
					format = "wav"
					found = true
					break
				}
			}
			if !found {
				return empty, errors.New("gemini TTS returned no audio")
			}
		}
		if err != nil {
			return empty, err
		}
		mt = ""
	}
	if len(raw) == 0 {
		return empty, errors.New("TTS provider returned empty audio")
	}
	if strings.Contains(mt, "application/json") || strings.HasPrefix(mt, "text/") {
		return empty, errors.New("TTS provider returned non-audio content")
	}
	if mt == "" || strings.HasPrefix(mt, "application/") {
		mt = audioMIME(format)
	}
	return attachment("audio", strings.Split(mt, ";")[0], raw), nil
}
func (s *Service) decodeAudio(ctx context.Context, r map[string]any) (chat.Attachment, error) {
	if a := object(r, "attachment"); a != nil {
		r = a
	}
	encoded := str(r, "data")
	if encoded == "" {
		encoded = str(r, "audio")
	}
	mt := str(r, "mime_type")
	if mt == "" {
		mt = "audio/wav"
	}
	if encoded != "" {
		if int64(base64.StdEncoding.DecodedLen(len(encoded))) > s.cfg.MaxBytes {
			return chat.Attachment{}, errors.New("command audio exceeds max_bytes")
		}
		b, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil {
			return chat.Attachment{}, e
		}
		if len(b) == 0 {
			return chat.Attachment{}, errors.New("command audio empty")
		}
		return attachment("audio", mt, b), nil
	}
	if u := str(r, "url"); u != "" {
		b, mt, e := s.load(ctx, chat.Attachment{Kind: "audio", URL: u, MIMEType: mt})
		if e != nil {
			return chat.Attachment{}, e
		}
		return attachment("audio", mt, b), nil
	}
	return chat.Attachment{}, errors.New("audio provider must return base64 data")
}
func audioMIME(format string) string {
	switch format {
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	case "opus", "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "aac":
		return "audio/aac"
	default:
		return "audio/mpeg"
	}
}
func pcmWAV(pcm []byte, rate int) []byte {
	b := make([]byte, 44+len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+len(pcm)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(pcm)))
	copy(b[44:], pcm)
	return b
}

var thinkPattern = regexp.MustCompile(`(?s)<think>.*?(?:</think>|$)`)
var codePattern = regexp.MustCompile("(?s)```.*?```")
var linkPattern = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

// NormalizeSpeech 排除思考块、代码和 Markdown 控制符，避免把私有推理朗读给用户。
func NormalizeSpeech(text string) string {
	text = thinkPattern.ReplaceAllString(text, "")
	text = codePattern.ReplaceAllString(text, "")
	text = linkPattern.ReplaceAllString(text, "$1")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if strings.ContainsRune("*_`#", r) {
			return -1
		}
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text))
}

// Speech 逐句交付音频，消费者失败或取消会停止后续合成；适用于平台与桌面流式回复。
func (s *Service) Speech(ctx context.Context, text string, emit func(chat.Attachment) error) error {
	if emit == nil {
		return errors.New("speech consumer required")
	}
	for _, part := range speechSentences(NormalizeSpeech(text)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		a, err := s.Synthesize(ctx, part)
		if err != nil {
			return err
		}
		if err = emit(a); err != nil {
			return err
		}
	}
	return nil
}
func speechSentences(text string) []string {
	var result []string
	start := 0
	for i, r := range text {
		if strings.ContainsRune(".!?。！？\n", r) && i-start >= 20 {
			end := i + len(string(r))
			result = append(result, text[start:end])
			start = end
		}
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		result = append(result, tail)
	}
	return result
}
