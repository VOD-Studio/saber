package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAudioStreamRequiresTaskBeforeUpgrade(t *testing.T) {
	for _, path := range []string{"/v1/audio/transcriptions/stream?session=voice", "/v1/audio/speech/stream?session=voice&task_id=0", "/v1/audio/speech/stream?session=../other&task_id=1"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Upgrade", "websocket")
		w := httptest.NewRecorder()
		(&handler{}).audioStream(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: upgraded without task, status %d", path, w.Code)
		}
	}
}
func TestAudioWebsocketRejectsURLCredential(t *testing.T) {
	h := New(nil, "private-token")
	r := httptest.NewRequest(http.MethodGet, "/v1/audio/transcriptions/stream?session=voice&task_id=1&token=private-token", nil)
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("URL credential accepted: %d", w.Code)
	}
}
