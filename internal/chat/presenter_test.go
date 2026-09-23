package chat_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

type sink struct {
	caps         chat.Capabilities
	sends, edits []chat.Reply
	fail         bool
}

func (s *sink) Capabilities() chat.Capabilities { return s.caps }
func (s *sink) Send(_ context.Context, r chat.Reply) (string, error) {
	if s.fail {
		s.fail = false
		return "", errors.New("temporary")
	}
	s.sends = append(s.sends, r)
	return "sent", nil
}
func (s *sink) Edit(_ context.Context, _ string, r chat.Reply) error {
	s.edits = append(s.edits, r)
	return nil
}
func (s *sink) SetTyping(context.Context, chat.Session, bool) error { return nil }

func TestPresenter_CapabilitiesAndRetryReset(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(map[bool]string{true: "editing", false: "final_only"}[edit], func(t *testing.T) {
			target := &sink{caps: chat.Capabilities{Edit: edit, Reply: true}}
			message := chat.Message{Session: chat.Session{Platform: "p", Account: "a", Conversation: "c"}, ID: "incoming"}
			presenter := chat.NewPresenter(target, message, chat.Display{MaxEdits: 1})
			ctx := context.Background()
			for _, e := range []agent.Event{{Kind: agent.ModelStarted}, {Kind: agent.TextDelta, Text: "failed attempt"}, {Kind: agent.AttemptStarted}, {Kind: agent.TextDelta, Text: "new"}, {Kind: agent.TextDelta, Text: " answer"}} {
				if err := presenter.Event(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			if err := presenter.Finish(ctx, "new answer"); err != nil {
				t.Fatal(err)
			}
			if err := presenter.Finish(ctx, "duplicate"); err != nil {
				t.Fatal(err)
			}
			if len(target.sends) != 1 || target.sends[0].ReplyTo != "incoming" {
				t.Fatalf("wrong reply: %+v", target.sends)
			}
			if edit {
				if len(target.edits) != 2 || target.edits[0].Text != "new" || target.edits[1].Text != "new answer" {
					t.Fatalf("retry text or final limit handling wrong: %+v", target.edits)
				}
			} else if target.sends[0].Text != "new answer" || len(target.edits) != 0 {
				t.Fatal("non-edit adapter got previews")
			}
		})
	}
}

func TestPresenter_ThresholdAndPreviewFailure(t *testing.T) {
	target := &sink{caps: chat.Capabilities{Edit: true}, fail: true}
	presenter := chat.NewPresenter(target, chat.Message{ID: "incoming"}, chat.Display{CharThreshold: 10, TimeThreshold: time.Hour, EditInterval: time.Hour})
	if err := presenter.Event(context.Background(), agent.Event{Kind: agent.TextDelta, Text: "short"}); err != nil {
		t.Fatal(err)
	}
	if !target.fail {
		t.Fatal("preview sent before threshold")
	}
	if err := presenter.Event(context.Background(), agent.Event{Kind: agent.TextDelta, Text: "long enough"}); err == nil {
		t.Fatal("preview failure not reported")
	}
	if err := presenter.Finish(context.Background(), "final"); err != nil {
		t.Fatal(err)
	}
	if len(target.sends) != 1 || target.sends[0].Text != "final" || target.sends[0].ReplyTo != "" {
		t.Fatalf("final delivery or reply capability wrong: %+v", target.sends)
	}
}

func TestPresenter_ReplyStateKeepsOneMessageAndPartialFailure(t *testing.T) {
	target := &sink{caps: chat.Capabilities{Edit: true, Reply: true, ReplyState: true}}
	p := chat.NewPresenter(target, chat.Message{Session: chat.Session{Platform: "p", Account: "a", Conversation: "c"}, ID: "incoming"}, chat.Display{})
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range []agent.Event{
		{Kind: agent.ModelStarted},
		{Kind: agent.ThinkingDelta, Text: "公开摘要"},
		{Kind: agent.TextDelta, Text: "部分"},
	} {
		if err := p.Event(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Fail(ctx, "timed_out"); err != nil {
		t.Fatal(err)
	}
	if err := p.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if len(target.sends) != 1 || target.sends[0].Status != chat.ReplyPending || target.sends[0].Text != "" || target.sends[0].ReplyTo != "incoming" {
		t.Fatalf("占位消息 = %+v", target.sends)
	}
	last := target.edits[len(target.edits)-1]
	if last.Status != chat.ReplyFailed || last.Text != "部分" || last.Thinking != "公开摘要" || last.ErrorCode != "timed_out" {
		t.Fatalf("失败回复 = %+v", last)
	}
}

func TestMessage_IdentityAndValidation(t *testing.T) {
	session := chat.Session{Platform: "p", Account: "a", Conversation: "c"}
	for _, m := range []chat.Message{{}, {Session: session}, {Session: session, SenderID: "42"}, {Session: session, SenderID: "42", Attachments: []chat.Attachment{{Kind: "file", URL: "x"}}}} {
		if m.Validate() == nil {
			t.Fatalf("invalid message accepted: %+v", m)
		}
	}
	m := chat.Message{Session: session, SenderID: "42", Attachments: []chat.Attachment{{Kind: "image", URL: "data:image/png;base64,AA=="}}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	identity := chat.Identity{Session: session, SenderID: "42"}
	got, ok := chat.IdentityFromContext(chat.WithIdentity(context.Background(), identity))
	if !ok || got != identity {
		t.Fatal("identity lost")
	}
	if _, ok := chat.IdentityFromContext(context.Background()); ok {
		t.Fatal("missing identity accepted")
	}
	if _, ok := chat.IdentityFromContext(chat.WithIdentity(context.Background(), chat.Identity{})); ok {
		t.Fatal("empty identity accepted")
	}
}
