package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

func mediaIdentity() context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: chat.Session{Platform: "test", Account: "bot", Conversation: "room"}, SenderID: "alice"})
}
func mediaFixture(t *testing.T, p config.MediaProviderConfig, check func(context.Context, string, bool) error) *Service {
	t.Helper()
	c := config.DefaultMediaToolsConfig()
	c.Enabled = true
	c.ASR = p
	c.TTS = p
	if check == nil {
		check = func(context.Context, string, bool) error { return nil }
	}
	s, err := New(c, toolkit.Host{StateDir: t.TempDir(), Check: check})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestTranscribeMultipartAndPrepareMessageIdentity(t *testing.T) {
	pcm := make([]byte, 3200)
	for i := 0; i < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], uint16(i))
	}
	wav := pcmWAV(pcm, 16000)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Errorf("wrong ASR wire: %s %v", r.URL.Path, r.Header)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		if r.FormValue("model") != "whisper-1" || r.FormValue("language") != "zh" {
			t.Error("trusted ASR configuration lost")
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = f.Close() }()
		b, err := io.ReadAll(f)
		if err != nil || !bytes.Equal(b, wav) {
			t.Error("ASR audio changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"今天下午三点开会","language":"zh","segments":[{"start":0,"end":0.1}]}`)
	}))
	defer server.Close()
	s := mediaFixture(t, config.MediaProviderConfig{Enabled: true, Provider: "openai", BaseURL: server.URL, APIKey: "private-test-key", Language: "zh"}, func(_ context.Context, name string, _ bool) error {
		if name == "exec" {
			return errors.New("exec is not granted")
		}
		return nil
	})
	ctx := mediaIdentity()
	id, _ := chat.IdentityFromContext(ctx)
	m := chat.Message{Session: id.Session, SenderID: id.SenderID, Attachments: []chat.Attachment{attachment("audio", "audio/wav", wav)}}
	prepared, err := s.PrepareMessage(ctx, m)
	if err != nil || !strings.Contains(prepared.Text, "今天下午三点开会") || len(prepared.Attachments) != 0 {
		t.Fatalf("actual audio was not consumed: %+v %v", prepared, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected request count: %d", calls.Load())
	}
	m.SenderID = "mallory"
	if _, err = s.PrepareMessage(ctx, m); err == nil {
		t.Fatal("identity mismatch accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("identity mismatch dispatched ASR")
	}
}

func TestSpeechCancellationDenialAndProviderErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"credentials private-test-key rejected"}`)
	}))
	defer server.Close()
	p := config.MediaProviderConfig{Enabled: true, Provider: "openai", BaseURL: server.URL, APIKey: "private-test-key"}
	s := mediaFixture(t, p, nil)
	if _, err := s.Synthesize(mediaIdentity(), "Hello there"); err == nil || strings.Contains(err.Error(), "private-test-key") {
		t.Fatalf("provider refusal or secret redaction lost: %v", err)
	}
	denied := mediaFixture(t, p, func(context.Context, string, bool) error { return errors.New("permission denied") })
	if _, err := denied.Transcribe(mediaIdentity(), attachment("audio", "audio/wav", pcmWAV([]byte{0, 0}, 16000))); err == nil {
		t.Fatal("denial swallowed")
	}
	if calls.Load() != 1 {
		t.Fatal("denied request was dispatched")
	}
	ctx, cancel := context.WithCancel(mediaIdentity())
	cancel()
	if _, err := s.Synthesize(ctx, "Hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled request dispatched")
	}
}

func TestGeneratedPixelsAndSSRFBoundary(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b.Bytes())
	}))
	defer server.Close()
	c := config.DefaultMediaToolsConfig()
	c.Enabled = true
	c.Image = config.MediaProviderConfig{Enabled: true, Provider: "openai"}
	s, err := New(c, toolkit.Host{StateDir: t.TempDir(), Check: func(context.Context, string, bool) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.generationResult(mediaIdentity(), map[string]any{"images": []any{map[string]any{"url": server.URL}}}, false); err == nil {
		t.Fatal("private media URL accepted without explicit allowlist")
	}
	r, err := s.generationResult(mediaIdentity(), map[string]any{"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(b.Bytes())}}}, false)
	if err != nil || len(r.Images) != 1 {
		t.Fatalf("valid image rejected: %v", err)
	}
	if _, err = s.generationResult(mediaIdentity(), map[string]any{"images": []any{"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not an image"))}}, false); err == nil {
		t.Fatal("non-image bytes became model pixels")
	}
}

func TestVADConfirmationSilenceAndStrictStop(t *testing.T) {
	c := config.DefaultMediaToolsConfig()
	v := NewVAD(c.Voice)
	tone := make([]byte, 640)
	for i := 0; i < len(tone); i += 2 {
		binary.LittleEndian.PutUint16(tone[i:], 1000)
	}
	for range 14 {
		_, started, ended := v.Push(tone)
		if started || ended {
			t.Fatal("speech confirmed too early")
		}
	}
	_, started, ended := v.Push(tone)
	if !started || ended {
		t.Fatal("sustained speech not confirmed")
	}
	silence := make([]byte, 640)
	for range 149 {
		_, _, ended = v.Push(silence)
		if ended {
			t.Fatal("silence ended early")
		}
	}
	_, _, ended = v.Push(silence)
	if !ended {
		t.Fatal("continuous silence did not end recording")
	}
	s := &Service{cfg: c}
	if !s.StopPhrase(" STOP! ") || s.StopPhrase("stop doing that") {
		t.Fatal("stop phrases must match whole utterances")
	}
	if got := NormalizeSpeech("<think>private reasoning</think> **Hello** [friend](https://example.com)\n```secret code``` "); strings.Contains(got, "private") || strings.Contains(got, "secret") || got != "Hello friend" {
		t.Fatalf("speech normalization leaked hidden content: %q", got)
	}
	if s.voiceTranscript("Thank you. Thank you!") != "" {
		t.Fatal("known silence hallucination not filtered")
	}
	s.cfg.Voice.StopPhrases = []string{"bye"}
	if s.voiceTranscript("bye") != "bye" {
		t.Fatal("explicit stop phrase must survive hallucination filter")
	}
}

func TestRealtimePCMCommitAndFinalTranscript(t *testing.T) {
	server := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		var configMessage map[string]any
		if err := websocket.JSON.Receive(conn, &configMessage); err != nil {
			t.Error(err)
			return
		}
		session := object(configMessage, "session")
		input := object(object(session, "audio"), "input")
		if str(object(input, "transcription"), "model") != "gpt-live-transcribe" {
			t.Error("streaming model missing")
		}
		var audio map[string]any
		if err := websocket.JSON.Receive(conn, &audio); err != nil {
			t.Error(err)
			return
		}
		pcm, err := base64.StdEncoding.DecodeString(str(audio, "audio"))
		if err != nil || len(pcm) != 4800 {
			t.Errorf("16kHz PCM not converted to 24kHz: %d %v", len(pcm), err)
		}
		var commit map[string]any
		if err = websocket.JSON.Receive(conn, &commit); err != nil {
			t.Error(err)
			return
		}
		if str(commit, "type") != "input_audio_buffer.commit" {
			t.Error("audio not committed")
		}
		for range 100 {
			if err = websocket.JSON.Send(conn, map[string]any{"type": "conversation.item.input_audio_transcription.delta", "delta": "x"}); err != nil {
				t.Error(err)
				return
			}
		}
		if err = websocket.JSON.Send(conn, map[string]any{"type": "conversation.item.input_audio_transcription.completed", "transcript": "a real final event"}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	s := mediaFixture(t, config.MediaProviderConfig{Enabled: true, Provider: "openai", BaseURL: server.URL, APIKey: "test-key"}, nil)
	ctx, cancel := context.WithTimeout(mediaIdentity(), 5*time.Second)
	defer cancel()
	stream, err := s.StartTranscription(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if err = stream.WritePCM(make([]byte, 3200)); err != nil {
		t.Fatal(err)
	}
	result, err := stream.Finish()
	if err != nil || result.Text != "a real final event" {
		t.Fatalf("final event lost: %+v %v", result, err)
	}
	if err = stream.WritePCM([]byte{0, 0}); err == nil {
		t.Fatal("post-commit audio accepted")
	}
}

func TestPersistentGenerationReceiptOwnership(t *testing.T) {
	c := config.DefaultMediaToolsConfig()
	c.Enabled = true
	c.Video = config.MediaProviderConfig{Enabled: true, Provider: "xai", BaseURL: "https://api.x.ai/v1", APIKey: "test-key"}
	host := toolkit.Host{StateDir: t.TempDir(), Check: func(context.Context, string, bool) error { return nil }}
	s, err := New(c, host)
	if err != nil {
		t.Fatal(err)
	}
	ctx := mediaIdentity()
	j := generationJob{ID: "request-123", Provider: "xai", StatusURL: "https://api.x.ai/v1/videos/request-123", ResultURL: "https://api.x.ai/v1/videos/request-123", Status: "interrupted", Video: true}
	if err = s.saveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(c, host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	result, err := restored.jobTool(ctx, map[string]any{"action": "status", "request_id": j.ID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if !bytes.Contains(encoded, []byte("interrupted")) {
		t.Fatal("receipt not durable")
	}
	other := chat.WithIdentity(context.Background(), chat.Identity{Session: chat.Session{Platform: "test", Account: "bot", Conversation: "room"}, SenderID: "mallory"})
	if _, err = restored.jobTool(other, map[string]any{"action": "status", "request_id": j.ID}); err == nil {
		t.Fatal("cross-user receipt readable")
	}
	if _, err = restored.jobTool(ctx, map[string]any{"action": "cancel", "request_id": j.ID}); err == nil {
		t.Fatal("unsupported remote cancellation claimed success")
	}
}
