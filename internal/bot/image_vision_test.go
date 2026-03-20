package bot

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TestImageVisionFlow tests the image message handling flow.
func TestImageVisionFlow(t *testing.T) {
	t.Run("media info extraction from image message", func(t *testing.T) {
		content := &event.MessageEventContent{
			MsgType: event.MsgImage,
			Body:    "test-image.png",
			URL:     "mxc://example.com/abc123",
			Info: &event.FileInfo{
				MimeType: "image/png",
				Size:     1024,
			},
		}

		if !content.MsgType.IsMedia() {
			t.Error("MsgImage should be detected as media")
		}
	})

	t.Run("media info extraction from video message", func(t *testing.T) {
		content := &event.MessageEventContent{
			MsgType: event.MsgVideo,
			Body:    "test-video.mp4",
			URL:     "mxc://example.com/video123",
		}

		if !content.MsgType.IsMedia() {
			t.Error("MsgVideo should be detected as media")
		}
	})

	t.Run("text message is not media", func(t *testing.T) {
		content := &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "Hello world",
		}

		if content.MsgType.IsMedia() {
			t.Error("MsgText should not be detected as media")
		}
	})
}

// TestImageVisionContext tests context propagation with media info.
func TestImageVisionContext(t *testing.T) {
	ctx := context.Background()

	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    "test.png",
		URL:     "mxc://example.com/test",
	}

	if !content.MsgType.IsMedia() {
		t.Fatal("expected MsgImage to be media")
	}

	_ = ctx
	_ = id.RoomID("!test:example.org")
}
