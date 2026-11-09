package media

import (
	"bytes"
	"context"
	"encoding/json"
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
)

func TestStreamingTransportFailureRecoversRecordedPCM(t *testing.T) {
	pcm := make([]byte, 3200)
	pcm[100] = 0x12
	wav := pcmWAV(pcm, 16000)
	var recovered atomic.Int32
	mux := http.NewServeMux()
	mux.Handle("/realtime", websocket.Handler(func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		for {
			var m map[string]any
			if err := websocket.JSON.Receive(conn, &m); err != nil {
				return
			}
		}
	}))
	mux.HandleFunc("/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = f.Close() }()
		raw, err := io.ReadAll(f)
		if err != nil || !bytes.Equal(raw, wav) {
			t.Error("stream fallback did not preserve exact microphone recording")
		}
		recovered.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"preserved recording"}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	s := mediaFixture(t, config.MediaProviderConfig{Enabled: true, Provider: "openai", BaseURL: server.URL, APIKey: "test-key"}, nil)
	ctx, cancel := context.WithTimeout(mediaIdentity(), 5*time.Second)
	defer cancel()
	st, err := s.StartTranscription(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_ = st.conn.Close()
	if err = st.WritePCM(pcm); err == nil {
		t.Fatal("closed transport should reject write")
	}
	result, err := st.Finish()
	if err != nil || result.Text != "preserved recording" || recovered.Load() != 1 {
		t.Fatalf("recording recovery failed: %+v %v %d", result, err, recovered.Load())
	}
}

func TestSpeechStreamSuppressesSplitHiddenMarkup(t *testing.T) {
	wav := pcmWAV(make([]byte, 3200), 16000)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Error(err)
			return
		}
		input := str(m, "input")
		if strings.Contains(input, "private") || strings.Contains(input, "secret") || strings.Contains(input, "<think") {
			t.Errorf("hidden content reached actual TTS request: %q", input)
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wav)
	}))
	defer server.Close()
	s := mediaFixture(t, config.MediaProviderConfig{Enabled: true, Provider: "openai", BaseURL: server.URL, APIKey: "test-key", Format: "wav"}, nil)
	deltas := make(chan string, 6)
	for _, delta := range []string{"<th", "ink>private reasoning </think>Hello this is the spoken sentence.", "```secret code```", "Another final sentence."} {
		deltas <- delta
	}
	close(deltas)
	played := 0
	if err := s.SpeechStream(mediaIdentity(), deltas, func(a chat.Attachment) error {
		played++
		if !bytes.Equal(a.Data, wav) || a.MIMEType != "audio/wav" {
			t.Fatal("TTS media changed before delivery")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || played != 2 {
		t.Fatalf("live sentence consumer did not deliver both real responses: %d %d", requests.Load(), played)
	}
}
