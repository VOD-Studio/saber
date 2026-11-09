package desktop

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDashboardAudioRejectsOriginAndInvalidTaskBeforeDial(t *testing.T) {
	handler := audioProxy("http://127.0.0.1:1", "private-token", "http://127.0.0.1:12345")
	for _, tc := range []struct {
		path, origin string
		status       int
	}{
		{"/bridge/audio?kind=transcriptions&session=test&task_id=1", "https://attacker.invalid", http.StatusForbidden},
		{"/bridge/audio?kind=transcriptions&session=test&task_id=0", "http://127.0.0.1:12345", http.StatusBadRequest},
		{"/bridge/audio?kind=other&session=test&task_id=1", "http://127.0.0.1:12345", http.StatusBadRequest},
		{"/bridge/audio?kind=speech&session=..%2Fsecret&task_id=1", "http://127.0.0.1:12345", http.StatusBadRequest},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.status)
		}
	}
}
