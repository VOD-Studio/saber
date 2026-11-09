package media

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// VAD 在 16kHz 单声道 PCM 上进行持续语音确认与结束检测，不依赖元数据。
type VAD struct {
	threshold                               float64
	speechSamples, silenceSamples, total    int
	speechTarget, silenceTarget, maxSamples int
	confirmed                               bool
}

// NewVAD 创建当前配置的两阶段语音活动检测器。
func NewVAD(c config.VoiceConfig) *VAD {
	return &VAD{threshold: c.SilenceThreshold, speechTarget: c.SpeechMS * 16, silenceTarget: c.SilenceMS * 16, maxSamples: c.MaxRecordSeconds * 16000}
}

// Push 返回该音频块的 RMS、首次确认语音和本轮录音结束状态。
func (v *VAD) Push(pcm []byte) (float64, bool, bool) {
	if len(pcm) < 2 {
		return 0, false, false
	}
	var energy float64
	n := len(pcm) / 2
	for i := 0; i+1 < len(pcm); i += 2 {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		energy += s * s
	}
	rms := math.Sqrt(energy / float64(n))
	was := v.confirmed
	v.total += n
	if rms >= v.threshold {
		v.speechSamples += n
		v.silenceSamples = 0
		if v.speechSamples >= v.speechTarget {
			v.confirmed = true
		}
	} else {
		v.silenceSamples += n
		if !v.confirmed && v.silenceSamples > 1600 {
			v.speechSamples = 0
		}
	}
	ended := v.total >= v.maxSamples || (v.confirmed && v.silenceSamples >= v.silenceTarget) || (!v.confirmed && v.total >= 15*16000)
	return rms, !was && v.confirmed, ended
}

var microphoneMu sync.Mutex

// ErrNoSpeech 区分静音与录音故障，连续语音及唤醒监听可以安全恢复而不上传静音。
var ErrNoSpeech = errors.New("microphone detected no speech")

// Capture 从显式配置的真实麦克风命令读取 PCM；命令只在授权任务执行环境运行。
// capture_command 应输出 16kHz 单声道 s16le（例如 arecord -f S16_LE -r 16000 -c 1），stderr 独立保留诊断。
func (s *Service) Capture(ctx context.Context) (chat.Attachment, error) { return s.capture(ctx, nil) }
func (s *Service) capture(ctx context.Context, onSpeech func()) (resultAttachment chat.Attachment, resultErr error) {
	var empty chat.Attachment
	v := s.cfg.Voice
	if !v.Enabled || len(v.CaptureCommand) == 0 {
		return empty, errors.New("voice microphone unavailable: configure media_tools.voice.capture_command in execution backend")
	}
	if err := s.authorize(ctx, "voice_capture", config.MediaProviderConfig{Enabled: true, Provider: "command"}); err != nil {
		return empty, err
	}
	if s.host.StartProcess == nil {
		return empty, errors.New("voice duplex execution backend unavailable")
	}
	if !microphoneMu.TryLock() {
		return empty, errors.New("microphone already held by a voice/wake listener")
	}
	defer microphoneMu.Unlock()
	ctx, done := s.scope(ctx)
	defer done()
	process, err := s.host.StartProcess(ctx, v.CaptureCommand, nil)
	if err != nil {
		return empty, err
	}
	defer func() { resultErr = errors.Join(resultErr, process.Close()) }()
	stop := context.AfterFunc(ctx, func() { _ = process.Close() })
	defer stop()
	vad := NewVAD(v)
	var pcm []byte
	frame := make([]byte, 640)
	for {
		n, readErr := io.ReadFull(process, frame)
		if n > 0 {
			pcm = append(pcm, frame[:n]...)
			if int64(len(pcm)) > s.cfg.MaxBytes {
				return empty, errors.New("microphone audio exceeds max_bytes")
			}
			if n%2 != 0 {
				return empty, errors.New("microphone returned incomplete s16le samples")
			}
			_, started, ended := vad.Push(frame[:n])
			if started && onSpeech != nil {
				onSpeech()
			}
			if ended {
				if !vad.confirmed {
					return empty, ErrNoSpeech
				}
				return attachment("audio", "audio/wav", pcmWAV(pcm, 16000)), nil
			}
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return empty, ctx.Err()
			}
			waitErr := process.Wait()
			if waitErr != nil {
				return empty, waitErr
			}
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
				return empty, readErr
			}
			if !vad.confirmed {
				return empty, ErrNoSpeech
			}
			return attachment("audio", "audio/wav", pcmWAV(pcm, 16000)), nil
		}
	}
}

// StopPhrase 仅匹配完整话语，避免把“stop doing that”当作结束会话。
func (s *Service) StopPhrase(text string) bool {
	normalize := func(v string) string {
		return strings.ToLower(strings.TrimFunc(strings.TrimSpace(v), func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) }))
	}
	text = normalize(text)
	for _, p := range s.cfg.Voice.StopPhrases {
		if text != "" && text == normalize(p) {
			return true
		}
	}
	return false
}

// DetectWake 使用配置的本地 KWS 命令，或已启用 whisper.cpp 的真实离线语音识别结果检测唤醒词。
func (s *Service) DetectWake(ctx context.Context, a chat.Attachment) (bool, error) {
	v := s.cfg.Voice
	if !v.Enabled || v.WakeWord == "" {
		return false, errors.New("wakeword disabled: configure media_tools.voice.wake_word")
	}
	p := config.MediaProviderConfig{Enabled: true, Provider: "command", Command: v.WakeCommand}
	if err := s.authorize(ctx, "wake_word", p); err != nil {
		return false, err
	}
	ctx, done := s.scope(ctx)
	defer done()
	if len(v.WakeCommand) == 0 {
		if !s.cfg.ASR.Enabled || s.cfg.ASR.Provider != "whisper_cpp" {
			return false, errors.New("wake detection requires a local wake_command or enabled whisper_cpp ASR; always-on audio is never implicitly uploaded")
		}
		t, err := s.Transcribe(ctx, a)
		if err != nil {
			return false, err
		}
		normalize := func(text string) string {
			return strings.ToLower(strings.Join(strings.FieldsFunc(text, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) }), " "))
		}
		text, phrase := normalize(t.Text), normalize(v.WakeWord)
		return phrase != "" && (text == phrase || strings.HasPrefix(text, phrase+" ")), nil
	}
	data, mt, err := s.load(ctx, a)
	if err != nil {
		return false, err
	}
	r, err := s.command(ctx, p, map[string]any{"operation": "wake", "phrase": v.WakeWord, "audio": base64.StdEncoding.EncodeToString(data), "mime_type": mt, "sample_rate": 16000})
	if err != nil {
		return false, err
	}
	detected, ok := r["detected"].(bool)
	if !ok {
		return false, errors.New("wake command must return boolean detected")
	}
	if phrase := str(r, "phrase"); detected && phrase != "" && !strings.EqualFold(phrase, v.WakeWord) {
		return false, errors.New("wake command detected a different phrase")
	}
	return detected, nil
}

// ListenWake 持续读取麦克风并调用真实 KWS；唤醒回调期间释放麦克风，恢复后应用冷却期。
func (s *Service) ListenWake(ctx context.Context, onWake func(context.Context) error) error {
	if onWake == nil {
		return errors.New("wake consumer required")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		a, err := s.Capture(ctx)
		if errors.Is(err, ErrNoSpeech) {
			continue
		}
		if err != nil {
			return err
		}
		detected, err := s.DetectWake(ctx, a)
		if err != nil {
			return err
		}
		if detected {
			if err = onWake(ctx); err != nil {
				return err
			}
			timer := time.NewTimer(time.Duration(s.cfg.Voice.WakeCooldownMS) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

// RunVoice 连接真实录音、转录、已授权聊天回合和语音消费者；新语音会取消前一轮回复。
func (s *Service) RunVoice(ctx context.Context, turn func(context.Context, string) (string, error), emit func(VoiceEvent) error, audio func(chat.Attachment) error) error {
	if turn == nil || emit == nil || audio == nil {
		return errors.New("voice turn, event and audio consumers required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var active context.CancelFunc
	var wg sync.WaitGroup
	defer func() {
		mu.Lock()
		if active != nil {
			active()
		}
		mu.Unlock()
		wg.Wait()
	}()
	var callbackMu sync.Mutex
	emitEvent := func(e VoiceEvent) error { callbackMu.Lock(); defer callbackMu.Unlock(); return emit(e) }
	emitAudio := func(a chat.Attachment) error { callbackMu.Lock(); defer callbackMu.Unlock(); return audio(a) }
	emptyTurns := 0
	for {
		a, err := s.capture(ctx, func() {
			if s.cfg.Voice.BargeIn {
				mu.Lock()
				if active != nil {
					active()
				}
				mu.Unlock()
			}
		})
		if errors.Is(err, ErrNoSpeech) {
			emptyTurns++
			if emptyTurns >= 3 {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
		t, err := s.Transcribe(ctx, a)
		if err != nil {
			return err
		}
		if s.StopPhrase(t.Text) {
			return nil
		}
		t.Text = s.voiceTranscript(t.Text)
		if t.Text == "" {
			emptyTurns++
			if emptyTurns >= 3 {
				return nil
			}
			continue
		}
		emptyTurns = 0
		if err = emitEvent(VoiceEvent{Type: "transcript", Text: t.Text, Provider: t.Provider}); err != nil {
			return err
		}
		mu.Lock()
		if active != nil {
			active()
		}
		replyCtx, replyCancel := context.WithCancel(ctx)
		active = replyCancel
		mu.Unlock()
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			defer replyCancel()
			reply, err := turn(replyCtx, text)
			if err == nil && s.cfg.TTS.Enabled {
				err = s.Speech(replyCtx, reply, emitAudio)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				if e := emitEvent(VoiceEvent{Type: "error", Error: err.Error()}); e != nil {
					cancel()
				}
			} else if err == nil {
				if e := emitEvent(VoiceEvent{Type: "reply", Text: reply}); e != nil {
					cancel()
				}
			}
		}(t.Text)
		if !s.cfg.Voice.BargeIn {
			wg.Wait()
		}
	}
}
