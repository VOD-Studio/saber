package media

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// VoiceEvent 是桌面、API 与麦克风消费者共用的转录事件；Error 不包含密钥。
type VoiceEvent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Error    string `json:"error,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// TranscriptionStream 接受 16kHz 单声道 s16le PCM，保留录音以便真实文件转录恢复。
type TranscriptionStream struct {
	service      *Service
	ctx          context.Context
	cancel       func()
	conn         *websocket.Conn
	provider     string
	mu           sync.Mutex
	pcm          []byte
	committed    bool
	closed       bool
	err          error
	events       chan VoiceEvent
	ready        chan struct{}
	readDone     chan struct{}
	chunks       []string
	final        Transcript
	phase        int
	closeOnce    sync.Once
	readyOnce    sync.Once
	finishOnce   sync.Once
	finishResult Transcript
	finishErr    error
}

// StartTranscription 打开当前配置 ASR 的真实流式协议；不将密钥发送给客户端。
func (s *Service) StartTranscription(ctx context.Context) (*TranscriptionStream, error) {
	p := s.cfg.ASR
	if err := s.authorize(ctx, "audio_transcribe", p); err != nil {
		return nil, err
	}
	if p.Provider != "openai" && p.Provider != "xai" && p.Provider != "elevenlabs" {
		return nil, fmt.Errorf("%s does not support native streaming ASR; use Transcribe", p.Provider)
	}
	ctx, cancel := s.scope(ctx)
	base, key, err := providerRuntime(p)
	if err != nil {
		cancel()
		return nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		cancel()
		return nil, err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	q := url.Values{}
	switch p.Provider {
	case "openai":
		u.Path += "/realtime"
		q.Set("intent", "transcription")
	case "xai":
		u.Path += "/stt"
		q.Set("sample_rate", "16000")
		q.Set("encoding", "pcm")
		q.Set("interim_results", "true")
		q.Set("model", model(p, "grok-voice-transcribe-2.0"))
		if p.Language != "" {
			q.Set("language", p.Language)
		}
	case "elevenlabs":
		u.Path += "/speech-to-text/realtime"
		q.Set("model_id", "scribe_v2_realtime")
		q.Set("audio_format", "pcm_16000")
		q.Set("commit_strategy", "vad")
		if p.Language != "" {
			q.Set("language_code", p.Language)
		}
	}
	u.RawQuery = q.Encode()
	cfg, err := websocket.NewConfig(u.String(), base)
	if err != nil {
		cancel()
		return nil, err
	}
	if p.Provider == "elevenlabs" {
		cfg.Header.Set("xi-api-key", key)
	} else {
		cfg.Header.Set("Authorization", "Bearer "+key)
	}
	connect, stop := context.WithTimeout(ctx, 10*time.Second)
	conn, err := cfg.DialContext(connect)
	stop()
	if err != nil {
		cancel()
		return nil, redactError(err, key)
	}
	conn.MaxPayloadBytes = 1024 * 1024
	st := &TranscriptionStream{service: s, ctx: ctx, cancel: cancel, conn: conn, provider: p.Provider, events: make(chan VoiceEvent, 64), ready: make(chan struct{}), readDone: make(chan struct{})}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close()
		cancel()
		return nil, errors.New("media closed")
	}
	s.streams[st] = struct{}{}
	s.mu.Unlock()
	if p.Provider == "openai" {
		m := p.StreamingModel
		if m == "" {
			m = "gpt-live-transcribe"
		}
		transcription := map[string]any{"model": m}
		if p.Language != "" {
			if m == "gpt-live-transcribe" {
				transcription["languages"] = []string{p.Language}
			} else {
				transcription["language"] = p.Language
			}
		}
		err = websocket.JSON.Send(conn, map[string]any{"type": "session.update", "session": map[string]any{"type": "transcription", "audio": map[string]any{"input": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}, "transcription": transcription, "turn_detection": nil}}}})
		if err != nil {
			_ = st.Close()
			return nil, err
		}
	}
	go st.read()
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-st.readDone:
		}
	}()
	if p.Provider == "xai" {
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-st.ready:
		case <-st.readDone:
			_ = st.Close()
			return nil, errors.New("xAI transcription closed before transcript.created")
		case <-ctx.Done():
			_ = st.Close()
			return nil, ctx.Err()
		case <-timer.C:
			_ = st.Close()
			return nil, errors.New("xAI transcript.created timeout")
		}
	}
	return st, nil
}

// Events 返回部分文本、最终转录和真实错误；消费者应同时监听请求上下文。
func (st *TranscriptionStream) Events() <-chan VoiceEvent { return st.events }
func (st *TranscriptionStream) emit(event VoiceEvent) bool {
	event.Provider = st.provider
	if st.ctx.Err() != nil {
		return false
	}
	select {
	case st.events <- event:
		return true
	default:
	}
	if event.Type == "partial" {
		return true
	}
	select {
	case <-st.events:
	default:
	}
	select {
	case st.events <- event:
		return true
	case <-st.ctx.Done():
		return false
	}
}
func (st *TranscriptionStream) read() {
	defer close(st.readDone)
	defer close(st.events)
	defer func() { _ = st.conn.Close() }()
	defer func() { st.service.mu.Lock(); delete(st.service.streams, st); st.service.mu.Unlock() }()
	var interim string
	for {
		var e map[string]any
		if err := websocket.JSON.Receive(st.conn, &e); err != nil {
			st.mu.Lock()
			st.err = errors.Join(st.err, err)
			closed := st.closed
			st.mu.Unlock()
			if !closed {
				st.emit(VoiceEvent{Type: "error", Error: err.Error()})
			}
			return
		}
		kind := str(e, "type")
		if kind == "" {
			kind = str(e, "message_type")
		}
		var text string
		final := false
		failure := ""
		switch st.provider {
		case "openai":
			switch {
			case strings.HasSuffix(kind, "input_audio_transcription.delta"):
				interim += str(e, "delta")
				text = interim
			case strings.HasSuffix(kind, "input_audio_transcription.completed"):
				text = str(e, "transcript")
				if text == "" {
					text = interim
				}
				final = true
			case kind == "error":
				failure = str(object(e, "error"), "message")
			}
		case "xai":
			switch kind {
			case "transcript.created":
				st.readyOnce.Do(func() { close(st.ready) })
				continue
			case "transcript.partial":
				text = str(e, "text")
				isFinal, _ := e["is_final"].(bool)
				speechFinal, _ := e["speech_final"].(bool)
				if isFinal || speechFinal {
					st.chunks = append(st.chunks, text)
					interim = ""
				} else {
					interim = text
				}
				text = strings.TrimSpace(strings.Join(st.chunks, " ") + " " + interim)
			case "transcript.done":
				text = strings.TrimSpace(strings.Join(st.chunks, " ") + " " + interim)
				final = true
			case "error":
				failure = str(e, "message")
			}
		case "elevenlabs":
			switch kind {
			case "partial_transcript":
				interim = str(e, "text")
				text = strings.TrimSpace(strings.Join(st.chunks, " ") + " " + interim)
			case "committed_transcript":
				if t := str(e, "text"); t != "" {
					st.chunks = append(st.chunks, t)
				}
				interim = ""
				text = strings.Join(st.chunks, " ")
				st.mu.Lock()
				final = st.committed
				st.mu.Unlock()
			case "error":
				failure = str(e, "error")
			default:
				if v := e["error"]; v != nil {
					failure = fmt.Sprint(v)
				}
			}
		}
		if failure != "" {
			st.mu.Lock()
			st.err = errors.Join(st.err, errors.New(failure))
			st.mu.Unlock()
			st.emit(VoiceEvent{Type: "error", Error: failure})
			return
		}
		if final {
			st.mu.Lock()
			st.final = Transcript{Text: strings.TrimSpace(text), Provider: st.provider}
			st.mu.Unlock()
			st.emit(VoiceEvent{Type: "final", Text: strings.TrimSpace(text)})
			return
		}
		if text != "" && !st.emit(VoiceEvent{Type: "partial", Text: text}) {
			return
		}
	}
}

// WritePCM 发送实际音频并保留原始录音；不会在已提交或关闭后继续发送。
func (st *TranscriptionStream) WritePCM(pcm []byte) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed || st.committed {
		return errors.New("transcription stream closed or committed")
	}
	if err := st.ctx.Err(); err != nil {
		return err
	}
	if len(pcm)%2 != 0 {
		return errors.New("PCM must contain complete s16le samples")
	}
	if int64(len(st.pcm)+len(pcm)) > st.service.cfg.MaxBytes {
		return errors.New("stream audio exceeds max_bytes")
	}
	st.pcm = append(st.pcm, pcm...)
	if st.err != nil {
		return st.err
	}
	switch st.provider {
	case "xai":
		return st.sendResult(websocket.Message.Send(st.conn, pcm))
	case "elevenlabs":
		return st.sendResult(websocket.JSON.Send(st.conn, map[string]any{"message_type": "input_audio_chunk", "audio_base_64": base64.StdEncoding.EncodeToString(pcm), "commit": false, "sample_rate": 16000}))
	default:
		// 16→24kHz 的整数相位转换跨块保持采样计数，避免每块重置导致音频漂移。
		out := make([]byte, 0, len(pcm)*3/2+2)
		for i := 0; i < len(pcm); i += 2 {
			sample := binary.LittleEndian.Uint16(pcm[i:])
			st.phase += 3
			for st.phase >= 2 {
				out = append(out, byte(sample), byte(sample>>8))
				st.phase -= 2
			}
		}
		return st.sendResult(websocket.JSON.Send(st.conn, map[string]any{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(out)}))
	}
}
func (st *TranscriptionStream) sendResult(err error) error {
	if err != nil {
		st.err = errors.Join(st.err, err)
	}
	return err
}

// Commit 完成当前录音；不会重新派发一段已提交音频。
func (st *TranscriptionStream) Commit() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return errors.New("transcription stream closed")
	}
	if st.committed {
		return st.err
	}
	st.committed = true
	if st.err != nil {
		return st.err
	}
	switch st.provider {
	case "xai":
		return st.sendResult(websocket.JSON.Send(st.conn, map[string]any{"type": "audio.done"}))
	case "elevenlabs":
		return st.sendResult(websocket.JSON.Send(st.conn, map[string]any{"message_type": "input_audio_chunk", "audio_base_64": "", "commit": true, "sample_rate": 16000}))
	default:
		return st.sendResult(websocket.JSON.Send(st.conn, map[string]any{"type": "input_audio_buffer.commit"}))
	}
}

// Finish 等待最终转录；仅流式协议失败时用保留的真实音频转录，用户取消不重试。
func (st *TranscriptionStream) Finish() (Transcript, error) {
	st.finishOnce.Do(func() { st.finishResult, st.finishErr = st.finish() })
	return st.finishResult, st.finishErr
}
func (st *TranscriptionStream) finish() (Transcript, error) {
	commitErr := st.Commit()
	if commitErr != nil {
		_ = st.conn.Close()
	}
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case <-st.readDone:
	case <-st.ctx.Done():
		return Transcript{}, st.ctx.Err()
	case <-timer.C:
		_ = st.conn.Close()
		<-st.readDone
		commitErr = errors.Join(commitErr, errors.New("stream final transcript timeout"))
	}
	st.mu.Lock()
	result := st.final
	err := errors.Join(commitErr, st.err)
	pcm := st.pcm
	st.mu.Unlock()
	if err == nil {
		return result, nil
	}
	if st.ctx.Err() != nil {
		return Transcript{}, st.ctx.Err()
	}
	if len(pcm) == 0 {
		return Transcript{}, err
	}
	fallback, fallbackErr := st.service.Transcribe(st.ctx, attachment("audio", "audio/wav", pcmWAV(pcm, 16000)))
	if fallbackErr != nil {
		return Transcript{}, errors.Join(err, fallbackErr)
	}
	return fallback, nil
}

// Close 关闭连接并取消录音，不做收费恢复请求。
func (st *TranscriptionStream) Close() error {
	var err error
	st.closeOnce.Do(func() {
		st.mu.Lock()
		st.closed = true
		st.mu.Unlock()
		st.cancel()
		err = st.conn.Close()
		st.service.mu.Lock()
		delete(st.service.streams, st)
		st.service.mu.Unlock()
	})
	return err
}

// CreateLiveSession 在宿主交换 GPT-Live WebRTC SDP，返回会话与回答，不返回 API 密钥。
func (s *Service) CreateLiveSession(ctx context.Context, sdp string) (map[string]any, error) {
	p := s.cfg.Voice.Live
	if !s.cfg.Voice.Enabled || s.cfg.Voice.Mode != "gpt-live" {
		return nil, errors.New("GPT-Live voice mode disabled")
	}
	if err := s.authorize(ctx, "voice_live", p); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(strings.TrimSpace(sdp), "v=0") || len(sdp) > 128*1024 {
		return nil, errors.New("invalid SDP offer")
	}
	ctx, done := s.scope(ctx)
	defer done()
	base, _, err := providerRuntime(p)
	if err != nil {
		return nil, err
	}
	voice := p.Voice
	if voice == "" {
		voice = "marin"
	}
	instructions := "You are the spoken interface of Saber. You have no tools. Delegate requests that require real work to Saber, and speak its actual result; never claim an action happened before it completes."
	if extra, ok := p.Options["instructions"].(string); ok {
		instructions += "\n" + extra
	}
	r, err := s.jsonRequest(ctx, p, "POST", base+"/live/sessions", map[string]any{"session": map[string]any{"model": model(p, "gpt-live-1"), "instructions": instructions, "audio": map[string]any{"output": map[string]any{"voice": voice}}, "delegation": map[string]any{"type": "client"}}, "transport": map[string]any{"type": "webrtc", "sdp": sdp}})
	if err != nil {
		return nil, err
	}
	session := object(r, "session")
	transport := object(r, "transport")
	if str(session, "id") == "" || str(transport, "sdp") == "" {
		return nil, errors.New("live provider returned no session id or SDP answer")
	}
	return map[string]any{"session": map[string]any{"id": str(session, "id")}, "transport": map[string]any{"type": "webrtc", "sdp": str(transport, "sdp")}}, nil
}
