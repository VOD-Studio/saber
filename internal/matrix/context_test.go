package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
)

// testContextKey 是测试专用的上下文键类型，避免与内置类型冲突。
type testContextKey string

const (
	testKey1 testContextKey = "key1"
	testKey2 testContextKey = "key2"
)

func TestContextEventID(t *testing.T) {
	t.Run("with valid EventID", func(t *testing.T) {
		ctx := context.Background()
		eventID := id.EventID("$1234567890abcdef:example.org")

		newCtx := WithEventID(ctx, eventID)
		got := GetEventID(newCtx)
		if got != eventID {
			t.Errorf("GetEventID() = %v, want %v", got, eventID)
		}
	})

	t.Run("with empty EventID", func(t *testing.T) {
		ctx := context.Background()
		emptyEventID := id.EventID("")

		newCtx := WithEventID(ctx, emptyEventID)
		got := GetEventID(newCtx)
		if got != emptyEventID {
			t.Errorf("GetEventID() = %v, want empty string", got)
		}
	})

	t.Run("with nil context value", func(t *testing.T) {
		ctx := context.Background()

		got := GetEventID(ctx)
		if got != "" {
			t.Errorf("GetEventID() = %v, want empty string", got)
		}
	})

	t.Run("context chain preservation", func(t *testing.T) {
		ctx := context.Background()
		eventID := id.EventID("$test:example.org")

		ctx1 := context.WithValue(ctx, testKey1, "value1")
		ctx2 := WithEventID(ctx1, eventID)
		ctx3 := context.WithValue(ctx2, testKey2, "value2")

		got := GetEventID(ctx3)
		if got != eventID {
			t.Errorf("GetEventID() = %v, want %v", got, eventID)
		}

		if v := ctx3.Value(testKey1); v != "value1" {
			t.Errorf("key1 = %v, want value1", v)
		}
		if v := ctx3.Value(testKey2); v != "value2" {
			t.Errorf("key2 = %v, want value2", v)
		}
	})

	t.Run("type safety - wrong type returns empty", func(t *testing.T) {
		ctx := context.Background()

		ctx = context.WithValue(ctx, eventIDKey, "wrong-type-string")

		got := GetEventID(ctx)
		if got != "" {
			t.Errorf("GetEventID() with wrong type = %v, want empty string", got)
		}
	})
}

func TestContextReferencedMediaInfo(t *testing.T) {
	t.Run("with valid MediaInfo", func(t *testing.T) {
		ctx := context.Background()
		mediaInfo := &MediaInfo{
			Type:     "image",
			MimeType: "image/png",
			Body:     "test.png",
		}

		newCtx := WithReferencedMediaInfo(ctx, mediaInfo)
		got := GetReferencedMediaInfo(newCtx)
		if got == nil {
			t.Fatal("GetReferencedMediaInfo() = nil, want non-nil")
		}
		if got.Type != "image" {
			t.Errorf("GetReferencedMediaInfo().Type = %v, want image", got.Type)
		}
		if got.MimeType != "image/png" {
			t.Errorf("GetReferencedMediaInfo().MimeType = %v, want image/png", got.MimeType)
		}
	})

	t.Run("with nil MediaInfo", func(t *testing.T) {
		ctx := context.Background()

		newCtx := WithReferencedMediaInfo(ctx, nil)
		got := GetReferencedMediaInfo(newCtx)
		if got != nil {
			t.Errorf("GetReferencedMediaInfo() = %v, want nil", got)
		}
	})

	t.Run("with empty context", func(t *testing.T) {
		ctx := context.Background()

		got := GetReferencedMediaInfo(ctx)
		if got != nil {
			t.Errorf("GetReferencedMediaInfo() = %v, want nil", got)
		}
	})

	t.Run("context chain preservation", func(t *testing.T) {
		ctx := context.Background()
		mediaInfo := &MediaInfo{
			Type:     "image",
			MimeType: "image/jpeg",
		}

		ctx1 := context.WithValue(ctx, testKey1, "value1")
		ctx2 := WithReferencedMediaInfo(ctx1, mediaInfo)
		ctx3 := context.WithValue(ctx2, testKey2, "value2")

		got := GetReferencedMediaInfo(ctx3)
		if got == nil {
			t.Error("GetReferencedMediaInfo() = nil, want non-nil")
		}

		if v := ctx3.Value(testKey1); v != "value1" {
			t.Errorf("key1 = %v, want value1", v)
		}
		if v := ctx3.Value(testKey2); v != "value2" {
			t.Errorf("key2 = %v, want value2", v)
		}
	})

	t.Run("type safety - wrong type returns nil", func(t *testing.T) {
		ctx := context.Background()

		ctx = context.WithValue(ctx, referencedMediaKey, "wrong-type-string")

		got := GetReferencedMediaInfo(ctx)
		if got != nil {
			t.Errorf("GetReferencedMediaInfo() with wrong type = %v, want nil", got)
		}
	})

	t.Run("independent from MediaInfo", func(t *testing.T) {
		ctx := context.Background()

		mediaInfo := &MediaInfo{Type: "image", MimeType: "image/png"}
		referencedMediaInfo := &MediaInfo{Type: "image", MimeType: "image/jpeg"}

		ctx = WithMediaInfo(ctx, mediaInfo)
		ctx = WithReferencedMediaInfo(ctx, referencedMediaInfo)

		gotMedia := GetMediaInfo(ctx)
		gotRefMedia := GetReferencedMediaInfo(ctx)

		if gotMedia == nil || gotMedia.MimeType != "image/png" {
			t.Errorf("GetMediaInfo() = %v, want image/png", gotMedia)
		}
		if gotRefMedia == nil || gotRefMedia.MimeType != "image/jpeg" {
			t.Errorf("GetReferencedMediaInfo() = %v, want image/jpeg", gotRefMedia)
		}
	})
}
