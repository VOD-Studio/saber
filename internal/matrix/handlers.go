// Package matrix provides Matrix event handling and command processing.
package matrix

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// CommandHandler defines the interface for handling bot commands.
type CommandHandler interface {
	// Handle processes a command with the given arguments.
	// ctx provides cancellation and timeout control.
	// userID is the Matrix ID of the user who sent the command.
	// roomID is the Matrix room ID where the command was sent.
	// args are the parsed command arguments (excluding the command itself).
	Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error
}

// CommandInfo contains metadata about a registered command.
type CommandInfo struct {
	Name        string
	Description string
	Handler     CommandHandler
}

// CommandService manages command registration and dispatch.
type CommandService struct {
	mu       sync.RWMutex
	commands map[string]CommandInfo
	client   *mautrix.Client
	botID    id.UserID
}

// NewCommandService creates a new command service.
func NewCommandService(client *mautrix.Client, botID id.UserID) *CommandService {
	return &CommandService{
		commands: make(map[string]CommandInfo),
		client:   client,
		botID:    botID,
	}
}

// RegisterCommand registers a command handler without a description.
// The command name should not include the prefix (!).
func (s *CommandService) RegisterCommand(cmd string, handler CommandHandler) {
	s.RegisterCommandWithDesc(cmd, "", handler)
}

// RegisterCommandWithDesc registers a command handler with a description.
// The command name should not include the prefix (!).
func (s *CommandService) RegisterCommandWithDesc(cmd, desc string, handler CommandHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.commands[strings.ToLower(cmd)] = CommandInfo{
		Name:        cmd,
		Description: desc,
		Handler:     handler,
	}

	slog.Debug("Registered command",
		"command", cmd,
		"description", desc)
}

// UnregisterCommand removes a command from the registry.
func (s *CommandService) UnregisterCommand(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.commands, strings.ToLower(cmd))
}

// GetCommand retrieves command info by name.
func (s *CommandService) GetCommand(cmd string) (CommandInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	info, ok := s.commands[strings.ToLower(cmd)]
	return info, ok
}

// ListCommands returns all registered commands.
func (s *CommandService) ListCommands() []CommandInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]CommandInfo, 0, len(s.commands))
	for _, info := range s.commands {
		list = append(list, info)
	}
	return list
}

// ParsedCommand represents a parsed command from a message.
type ParsedCommand struct {
	Command string
	Args    []string
}

// ParseCommand extracts a command and arguments from a message body.
// Supports prefix-based commands (!command args) and mentions (@bot:command args).
// Returns nil if the message is not a valid command.
func (s *CommandService) ParseCommand(body string) *ParsedCommand {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}

	// Check for prefix-based command (!command)
	if strings.HasPrefix(body, "!") {
		return s.parsePrefixedCommand(body[1:])
	}

	// Check for mention-based command (@bot:command)
	if strings.HasPrefix(body, "@") {
		return s.parseMentionCommand(body)
	}

	return nil
}

func (s *CommandService) parsePrefixedCommand(body string) *ParsedCommand {
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return nil
	}

	return &ParsedCommand{
		Command: strings.ToLower(parts[0]),
		Args:    parts[1:],
	}
}

func (s *CommandService) parseMentionCommand(body string) *ParsedCommand {
	// Format: @bot:server.com command args
	// or: @bot:server.com: command args
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return nil
	}

	// First part should be the mention
	mention := parts[0]

	// Verify it's a mention of our bot
	expectedMention := string(s.botID)
	if mention != expectedMention {
		// Check for mention with trailing colon
		if strings.TrimSuffix(mention, ":") != expectedMention {
			return nil
		}
	}

	// Remaining parts are the command and args
	if len(parts) < 2 {
		return nil
	}

	return &ParsedCommand{
		Command: strings.ToLower(parts[1]),
		Args:    parts[2:],
	}
}

// HandleEvent processes a Matrix event and dispatches commands.
// It only handles message events and ignores events from the bot itself.
func (s *CommandService) HandleEvent(ctx context.Context, evt *event.Event) error {
	// Only handle room messages
	if evt.Type != event.EventMessage {
		return nil
	}

	// Parse message content
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok {
		return nil
	}

	// Ignore edits
	if content.RelatesTo != nil && content.RelatesTo.Type == event.RelReplace {
		slog.Debug("Ignoring edited message", "event_id", evt.ID.String())
		return nil
	}

	// Ignore own messages
	sender := evt.Sender
	if sender == s.botID {
		return nil
	}

	roomID := evt.RoomID

	// Log received message
	slog.Info("Received message",
		"sender", sender.String(),
		"room", roomID.String(),
		"event_id", evt.ID.String(),
		"body", content.Body)

	// Parse command
	parsed := s.ParseCommand(content.Body)
	if parsed == nil {
		return nil
	}

	// Look up command
	cmdInfo, ok := s.GetCommand(parsed.Command)
	if !ok {
		slog.Debug("Unknown command", "command", parsed.Command)
		return nil
	}

	// Log command execution
	slog.Info("Executing command",
		"command", parsed.Command,
		"sender", sender.String(),
		"room", roomID.String(),
		"args", parsed.Args)

	// Execute command
	err := cmdInfo.Handler.Handle(ctx, sender, roomID, parsed.Args)
	if err != nil {
		slog.Error("Command execution failed",
			"command", parsed.Command,
			"sender", sender.String(),
			"error", err)

		// Report error to room
		return s.reportError(ctx, roomID, parsed.Command, err)
	}

	return nil
}

func (s *CommandService) reportError(ctx context.Context, roomID id.RoomID, cmd string, err error) error {
	msg := fmt.Sprintf("Error executing command '%s': %v", cmd, err)

	_, sendErr := s.client.SendMessageEvent(
		ctx,
		roomID,
		event.EventMessage,
		&event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    msg,
		},
	)

	if sendErr != nil {
		slog.Error("Failed to send error message to room",
			"room", roomID.String(),
			"error", sendErr)
		return fmt.Errorf("command error: %v, send error: %w", err, sendErr)
	}

	return err
}

// SendText sends a text message to a room.
func (s *CommandService) SendText(ctx context.Context, roomID id.RoomID, body string) error {
	_, err := s.client.SendMessageEvent(
		ctx,
		roomID,
		event.EventMessage,
		&event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    body,
		},
	)

	if err != nil {
		slog.Error("Failed to send message",
			"room", roomID.String(),
			"error", err)
	}

	return err
}

// EventHandler wraps CommandService and implements mautrix event handling.
type EventHandler struct {
	service *CommandService
	logger  *slog.Logger
}

// NewEventHandler creates a new event handler.
func NewEventHandler(service *CommandService) *EventHandler {
	return &EventHandler{
		service: service,
		logger:  slog.With("component", "event_handler"),
	}
}

// OnMessage handles incoming message events.
// This is designed to be used as the Syncer.OnEvent callback.
func (h *EventHandler) OnMessage(ctx context.Context, evt *event.Event) {
	logger := h.logger.With(
		"event_id", evt.ID.String(),
		"type", evt.Type.String(),
		"sender", evt.Sender.String())

	logger.Debug("Processing event")

	if err := h.service.HandleEvent(ctx, evt); err != nil {
		logger.Error("Event handling failed", "error", err)
	}
}

// OnEvent is a generic event handler that dispatches to appropriate handlers.
func (h *EventHandler) OnEvent(ctx context.Context, evt *event.Event) {
	switch evt.Type {
	case event.EventMessage:
		h.OnMessage(ctx, evt)
	default:
		h.logger.Debug("Ignoring non-message event", "type", evt.Type.String())
	}
}

// Service returns the underlying CommandService.
func (h *EventHandler) Service() *CommandService {
	return h.service
}

// Built-in commands

// PingCommand responds with "Pong!".
type PingCommand struct {
	service *CommandService
}

// NewPingCommand creates a new ping command handler.
func NewPingCommand(service *CommandService) *PingCommand {
	return &PingCommand{service: service}
}

// Handle implements CommandHandler.
func (c *PingCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	return c.service.SendText(ctx, roomID, "Pong!")
}

// HelpCommand lists available commands.
type HelpCommand struct {
	service *CommandService
}

// NewHelpCommand creates a new help command handler.
func NewHelpCommand(service *CommandService) *HelpCommand {
	return &HelpCommand{service: service}
}

// Handle implements CommandHandler.
func (c *HelpCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	commands := c.service.ListCommands()

	if len(commands) == 0 {
		return c.service.SendText(ctx, roomID, "No commands available.")
	}

	var sb strings.Builder
	sb.WriteString("Available commands:\n")

	for _, cmd := range commands {
		sb.WriteString(fmt.Sprintf("  !%s", cmd.Name))
		if cmd.Description != "" {
			sb.WriteString(fmt.Sprintf(" - %s", cmd.Description))
		}
		sb.WriteString("\n")
	}

	return c.service.SendText(ctx, roomID, sb.String())
}

// RegisterBuiltinCommands registers the default commands (!ping, !help).
func RegisterBuiltinCommands(service *CommandService) {
	service.RegisterCommandWithDesc("ping", "Check if bot is alive", NewPingCommand(service))
	service.RegisterCommandWithDesc("help", "List available commands", NewHelpCommand(service))
}
