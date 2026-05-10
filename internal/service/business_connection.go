package service

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"removed-messages/internal/storage"
)

// HandleBusinessConnection processes a `business_connection` update from Telegram. The bot uses
// this to know which Telegram Business accounts have currently connected the bot and whether the
// connection is enabled or has been disabled by the owner. The full state is persisted to SQLite
// so it survives restarts.
func (s *BusinessMessageService) HandleBusinessConnection(ctx context.Context, conn *models.BusinessConnection) {
	if s == nil || conn == nil {
		return
	}
	if strings.TrimSpace(conn.ID) == "" {
		s.logger.Warnf("skip business connection update with empty id")
		return
	}

	now := time.Now().UTC()
	connectedAt := now
	if conn.Date > 0 {
		connectedAt = time.Unix(conn.Date, 0).UTC()
	}

	canReply := false
	if conn.Rights != nil {
		canReply = conn.Rights.CanReply
	}

	previous, err := s.repo.GetBusinessConnection(ctx, conn.ID)
	if err != nil {
		s.logger.Warnf("load previous business connection failed business_connection_id=%s: %v", conn.ID, err)
	}

	record := &storage.BusinessConnection{
		ID:               conn.ID,
		OwnerUserID:      conn.User.ID,
		OwnerUserChatID:  conn.UserChatID,
		OwnerUsername:    normalizeUsernameForStorage(conn.User.Username),
		OwnerDisplayName: buildDisplayNameFromUser(&conn.User),
		IsEnabled:        conn.IsEnabled,
		CanReply:         canReply,
		ConnectedAt:      connectedAt,
		UpdatedAt:        now,
	}
	if !conn.IsEnabled {
		record.DisconnectedAt = &now
	}
	if previous != nil {
		record.ConnectedAt = previous.ConnectedAt // preserve original connect timestamp
		if conn.IsEnabled {
			record.DisconnectedAt = nil
		}
	}

	if err := s.repo.UpsertBusinessConnection(ctx, record); err != nil {
		s.logger.Errorf("upsert business connection failed business_connection_id=%s: %v", conn.ID, err)
		return
	}

	s.logger.Infof("business connection update business_connection_id=%s owner_user_id=%d owner_username=%s is_enabled=%t can_reply=%t",
		record.ID, record.OwnerUserID, record.OwnerUsername, record.IsEnabled, record.CanReply)

	stateChanged := previous == nil || previous.IsEnabled != record.IsEnabled || previous.CanReply != record.CanReply
	if stateChanged && s.cfg.OwnerChatID != 0 {
		text := s.buildBusinessConnectionNotification(previous, record)
		if err := s.tg.SendOwnerNotification(ctx, s.cfg.OwnerChatID, text); err != nil {
			s.logger.Warnf("notify owner of business connection change failed business_connection_id=%s: %v", record.ID, err)
		}
	}
}

func (s *BusinessMessageService) buildBusinessConnectionNotification(previous *storage.BusinessConnection, current *storage.BusinessConnection) string {
	if current == nil {
		return "<b>Business connection update</b>"
	}

	var title string
	switch {
	case previous == nil:
		if current.IsEnabled {
			title = "<b>Business connection established</b>"
		} else {
			title = "<b>Business connection received (disabled)</b>"
		}
	case !previous.IsEnabled && current.IsEnabled:
		title = "<b>Business connection re-enabled</b>"
	case previous.IsEnabled && !current.IsEnabled:
		title = "<b>Business connection disabled</b>"
	default:
		title = "<b>Business connection rights changed</b>"
	}

	username := formatUsernameOrPlaceholder(current.OwnerUsername)
	lines := []string{
		title,
		fmt.Sprintf("Connection ID: <code>%s</code>", html.EscapeString(current.ID)),
		fmt.Sprintf("Owner: <code>%s</code>", html.EscapeString(username)),
	}
	if name := compactSingleLine(current.OwnerDisplayName); name != "" {
		lines = append(lines, fmt.Sprintf("Display name: <code>%s</code>", html.EscapeString(name)))
	}
	lines = append(lines,
		fmt.Sprintf("Owner user ID: <code>%d</code>", current.OwnerUserID),
		fmt.Sprintf("Is enabled: <code>%t</code>", current.IsEnabled),
		fmt.Sprintf("Can reply: <code>%t</code>", current.CanReply),
		fmt.Sprintf("Connected at (UTC): <code>%s</code>", current.ConnectedAt.UTC().Format(time.RFC3339)),
	)
	return strings.Join(lines, "\n")
}

// isBusinessConnectionActive reports whether the bot currently has an enabled connection for the
// given id. Returns true when no record exists yet (race between business_connection and the first
// message); the message is still archived but the gap is logged.
func (s *BusinessMessageService) isBusinessConnectionActive(ctx context.Context, id string) bool {
	if s == nil || strings.TrimSpace(id) == "" {
		return false
	}
	record, err := s.repo.GetBusinessConnection(ctx, id)
	if err != nil {
		s.logger.Warnf("business connection lookup failed business_connection_id=%s: %v", id, err)
		return true // fail-open: do not drop messages on transient lookup errors
	}
	if record == nil {
		s.logger.Warnf("business connection not yet known business_connection_id=%s — archiving anyway", id)
		return true
	}
	return record.IsEnabled
}
