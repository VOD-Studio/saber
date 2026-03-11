## OnMember Implementation Pattern

Learned from implementing `OnMember` method in `internal/matrix/handlers.go`:

### Event Type Constants in Mautrix

- Room messages use: `event.EventMessage`
- Room member state changes use: `event.StateMember` (NOT `event.EventMember`)
- Ephemeral events use: `event.EphemeralEventTyping`

### Member Event Processing Pattern

```go
func (h *EventHandler) OnMember(ctx context.Context, evt *event.Event) {
    // 1. Parse event content as *event.MemberEventContent
    content, ok := evt.Content.Parsed.(*event.MemberEventContent)
    
    // 2. Check membership type (Invite, Join, Leave, Ban, Knock)
    if content.Membership != event.MembershipInvite {
        return
    }
    
    // 3. Check StateKey for target user ID
    if evt.StateKey == nil {
        return
    }
    targetUserID := id.UserID(*evt.StateKey)
    
    // 4. Verify invite is for bot itself
    if targetUserID != h.service.botID {
        return
    }
    
    // 5. Accept invite with JoinRoom
    _, err := h.service.client.JoinRoom(ctx, evt.RoomID.String(), nil)
}
```

### Key Fields in Member Events

- `evt.StateKey`: Target user ID of the membership change (pointer to string)
- `evt.Sender`: User who initiated the membership change (inviter)
- `evt.RoomID`: Room where the membership change occurred
- `content.Membership`: Type of membership (Invite, Join, Leave, Ban, Knock)

### Comment Style

All comments must be in Chinese following project's AGENTS.md guidelines:
- Explain WHY, not WHAT
- Use structured logging with slog
- Log at appropriate levels (Debug for filtering, Info for actions, Error for failures)

## Testing Patterns for Matrix Event Handlers

### HTTP Client Mocking Strategy

Since mautrix.Client is a struct (not an interface), unit tests should:
1. Create a real `*mautrix.Client` instance
2. Use a mock `http.RoundTripper` to control HTTP responses
3. Assign the mock transport to `client.Client.Transport`
4. This allows testing without network calls while using the real client structure

### Test Structure

Table-driven tests with comprehensive edge case coverage:
- Valid invite to bot (happy path)
- Invite to other users (should ignore)
- All membership types (Invite, Join, Leave, Ban, Knock)
- Nil StateKey handling
- Invalid content type handling
- Error handling for JoinRoom failures

### Validation Approach

Instead of mocking the JoinRoom method directly, validate that:
- HTTP requests are made when expected
- No HTTP requests are made when processing should be skipped
- Error cases are handled gracefully without panics

This approach provides realistic testing while maintaining isolation from network dependencies.