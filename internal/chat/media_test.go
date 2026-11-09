package chat_test

import (
	"testing"

	"rua.plus/saber/internal/chat"
)

func TestCanonicalMediaAttachments(t *testing.T) {
	session := chat.Session{Platform: "test", Account: "bot", Conversation: "room"}
	for _, kind := range []string{"image", "audio", "video", "file"} {
		m := chat.Message{Session: session, SenderID: "alice", Attachments: []chat.Attachment{{Kind: kind, Data: []byte{1, 2, 3}, Size: 3}}}
		if err := m.Validate(); err != nil {
			t.Errorf("%s payload rejected: %v", kind, err)
		}
	}
	for _, a := range []chat.Attachment{{Kind: "audio"}, {Kind: "audio", Data: []byte{1}, Size: 2}, {Kind: "video", URL: "file:///etc/passwd"}, {Kind: "file", URL: "https://token:secret@example.org/private"}, {Kind: "script", Data: []byte("x")}, {Kind: "image", URL: "data:image/png,not-base64"}} {
		if err := a.Validate(); err == nil {
			t.Errorf("invalid canonical media accepted: %+v", a)
		}
	}
}
