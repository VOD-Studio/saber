// Package matrix provides Matrix client functionality for the Saber bot.
// This file contains room operations: join, leave, send messages, and room info.
package matrix

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type RoomInfo struct {
	ID          id.RoomID
	Alias       string
	Name        string
	Topic       string
	MemberCount int
	AvatarURL   id.ContentURIString
	IsEncrypted bool
}

type RoomService struct {
	client *MatrixClient
	log    zerolog.Logger
}

func NewRoomService(client *MatrixClient) *RoomService {
	return &RoomService{
		client: client,
		log:    log.With().Str("component", "room_service").Logger(),
	}
}

func (rs *RoomService) JoinRoom(ctx context.Context, roomIDOrAlias string) (*RoomInfo, error) {
	if roomIDOrAlias == "" {
		return nil, errors.New("room ID or alias cannot be empty")
	}

	rs.log.Info().Str("room", roomIDOrAlias).Msg("Joining room")

	var alias string
	if strings.HasPrefix(roomIDOrAlias, "!") {
	} else if strings.HasPrefix(roomIDOrAlias, "#") {
		alias = roomIDOrAlias
	} else {
		return nil, fmt.Errorf("invalid room identifier: %s (must start with ! for room ID or # for alias)", roomIDOrAlias)
	}

	joinReq := &mautrix.ReqJoinRoom{}
	joinedRoom, err := rs.client.GetClient().JoinRoom(ctx, roomIDOrAlias, joinReq)
	if err != nil {
		rs.log.Error().Err(err).Str("room", roomIDOrAlias).Msg("Failed to join room")
		return nil, fmt.Errorf("failed to join room %s: %w", roomIDOrAlias, err)
	}

	roomID := joinedRoom.RoomID
	rs.log.Info().Str("room_id", roomID.String()).Str("alias", alias).Msg("Successfully joined room")

	info, err := rs.GetRoomInfo(ctx, roomID.String())
	if err != nil {
		return &RoomInfo{ID: roomID, Alias: alias}, nil
	}

	if alias != "" {
		info.Alias = alias
	}

	return info, nil
}

func (rs *RoomService) LeaveRoom(ctx context.Context, roomID string) error {
	if roomID == "" {
		return errors.New("room ID cannot be empty")
	}

	rs.log.Info().Str("room_id", roomID).Msg("Leaving room")

	leaveReq := &mautrix.ReqLeave{}
	_, err := rs.client.GetClient().LeaveRoom(ctx, id.RoomID(roomID), leaveReq)
	if err != nil {
		rs.log.Error().Err(err).Str("room_id", roomID).Msg("Failed to leave room")
		return fmt.Errorf("failed to leave room %s: %w", roomID, err)
	}

	rs.log.Info().Str("room_id", roomID).Msg("Successfully left room")
	return nil
}

func (rs *RoomService) SendMessage(ctx context.Context, roomID, text string) (id.EventID, error) {
	if roomID == "" {
		return "", errors.New("room ID cannot be empty")
	}
	if text == "" {
		return "", errors.New("message text cannot be empty")
	}

	rs.log.Debug().Str("room_id", roomID).Int("text_len", len(text)).Msg("Sending text message")

	resp, err := rs.client.GetClient().SendText(ctx, id.RoomID(roomID), text)
	if err != nil {
		rs.log.Error().Err(err).Str("room_id", roomID).Msg("Failed to send message")
		return "", fmt.Errorf("failed to send message to room %s: %w", roomID, err)
	}

	rs.log.Info().Str("room_id", roomID).Str("event_id", resp.EventID.String()).Msg("Message sent successfully")
	return resp.EventID, nil
}

func (rs *RoomService) SendFormattedMessage(ctx context.Context, roomID, html, plain string) (id.EventID, error) {
	if roomID == "" {
		return "", errors.New("room ID cannot be empty")
	}
	if html == "" {
		return "", errors.New("HTML content cannot be empty")
	}
	if plain == "" {
		return "", errors.New("plain text content cannot be empty")
	}

	rs.log.Debug().Str("room_id", roomID).Int("html_len", len(html)).Int("plain_len", len(plain)).Msg("Sending formatted message")

	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          plain,
		Format:        event.FormatHTML,
		FormattedBody: html,
	}

	resp, err := rs.client.GetClient().SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, content)
	if err != nil {
		rs.log.Error().Err(err).Str("room_id", roomID).Msg("Failed to send formatted message")
		return "", fmt.Errorf("failed to send formatted message to room %s: %w", roomID, err)
	}

	rs.log.Info().Str("room_id", roomID).Str("event_id", resp.EventID.String()).Msg("Formatted message sent successfully")
	return resp.EventID, nil
}

func (rs *RoomService) SendNotice(ctx context.Context, roomID, text string) (id.EventID, error) {
	if roomID == "" {
		return "", errors.New("room ID cannot be empty")
	}
	if text == "" {
		return "", errors.New("notice text cannot be empty")
	}

	rs.log.Debug().Str("room_id", roomID).Int("text_len", len(text)).Msg("Sending notice message")

	resp, err := rs.client.GetClient().SendNotice(ctx, id.RoomID(roomID), text)
	if err != nil {
		rs.log.Error().Err(err).Str("room_id", roomID).Msg("Failed to send notice")
		return "", fmt.Errorf("failed to send notice to room %s: %w", roomID, err)
	}

	rs.log.Info().Str("room_id", roomID).Str("event_id", resp.EventID.String()).Msg("Notice sent successfully")
	return resp.EventID, nil
}

func (rs *RoomService) GetJoinedRooms(ctx context.Context) ([]RoomInfo, error) {
	rs.log.Debug().Msg("Fetching joined rooms")

	resp, err := rs.client.GetClient().JoinedRooms(ctx)
	if err != nil {
		rs.log.Error().Err(err).Msg("Failed to get joined rooms")
		return nil, fmt.Errorf("failed to get joined rooms: %w", err)
	}

	rooms := make([]RoomInfo, 0, len(resp.JoinedRooms))
	for _, roomID := range resp.JoinedRooms {
		info, err := rs.GetRoomInfo(ctx, roomID.String())
		if err != nil {
			rs.log.Warn().Err(err).Str("room_id", roomID.String()).Msg("Failed to get room info, using basic info")
			rooms = append(rooms, RoomInfo{ID: roomID})
			continue
		}
		rooms = append(rooms, *info)
	}

	rs.log.Info().Int("count", len(rooms)).Msg("Retrieved joined rooms")
	return rooms, nil
}

func (rs *RoomService) GetRoomInfo(ctx context.Context, roomID string) (*RoomInfo, error) {
	if roomID == "" {
		return nil, errors.New("room ID cannot be empty")
	}

	rs.log.Debug().Str("room_id", roomID).Msg("Fetching room info")

	info := &RoomInfo{ID: id.RoomID(roomID)}
	client := rs.client.GetClient()
	roomIDTyped := id.RoomID(roomID)

	// Get room name
	if nameEv, err := client.FullStateEvent(ctx, roomIDTyped, event.StateRoomName, ""); err == nil {
		if nameContent, ok := nameEv.Content.Parsed.(*event.RoomNameEventContent); ok {
			info.Name = nameContent.Name
		}
	}

	// Get room topic
	if topicEv, err := client.FullStateEvent(ctx, roomIDTyped, event.StateTopic, ""); err == nil {
		if topicContent, ok := topicEv.Content.Parsed.(*event.TopicEventContent); ok {
			info.Topic = topicContent.Topic
		}
	}

	// Get canonical alias
	if aliasEv, err := client.FullStateEvent(ctx, roomIDTyped, event.StateCanonicalAlias, ""); err == nil {
		if aliasContent, ok := aliasEv.Content.Parsed.(*event.CanonicalAliasEventContent); ok {
			info.Alias = aliasContent.Alias.String()
		}
	}

	// Get avatar URL
	if avatarEv, err := client.FullStateEvent(ctx, roomIDTyped, event.StateRoomAvatar, ""); err == nil {
		if avatarContent, ok := avatarEv.Content.Parsed.(*event.RoomAvatarEventContent); ok {
			info.AvatarURL = avatarContent.URL
		}
	}

	// Get encryption status
	if _, err := client.FullStateEvent(ctx, roomIDTyped, event.StateEncryption, ""); err == nil {
		info.IsEncrypted = true
	}

	// Get member count from state
	stateMap, err := client.State(ctx, roomIDTyped)
	if err == nil {
		if memberEvents, ok := stateMap[event.StateMember]; ok {
			memberCount := 0
			for _, evt := range memberEvents {
				if evt != nil {
					if memberContent, ok := evt.Content.Parsed.(*event.MemberEventContent); ok {
						if memberContent.Membership == event.MembershipJoin {
							memberCount++
						}
					}
				}
			}
			info.MemberCount = memberCount
		}
	}

	rs.log.Debug().
		Str("room_id", roomID).
		Str("name", info.Name).
		Str("alias", info.Alias).
		Int("members", info.MemberCount).
		Bool("encrypted", info.IsEncrypted).
		Msg("Room info retrieved")

	return info, nil
}

func (rs *RoomService) SetLogger(logger zerolog.Logger) {
	rs.log = logger
}
