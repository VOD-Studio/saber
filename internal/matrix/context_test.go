package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
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

		ctx1 := context.WithValue(ctx, "key1", "value1")
		ctx2 := WithEventID(ctx1, eventID)
		ctx3 := context.WithValue(ctx2, "key2", "value2")

		got := GetEventID(ctx3)
		if got != eventID {
			t.Errorf("GetEventID() = %v, want %v", got, eventID)
		}

		if v := ctx3.Value("key1"); v != "value1" {
			t.Errorf("key1 = %v, want value1", v)
		}
		if v := ctx3.Value("key2"); v != "value2" {
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
