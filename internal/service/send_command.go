package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/go-telegram/bot/models"
)

const sendCommandUsage = "Usage:\n/send [@username, @username] [text]"

var usernameKeyPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

type sendCommandRequest struct {
	Recipients []string
	Text       string
}

type sendCommandFailure struct {
	Recipient string
	Reason    string
}

func (s *BusinessMessageService) HandleOwnerCommand(ctx context.Context, message *models.Message) bool {
	if s == nil || message == nil {
		return false
	}

	if isConnectionsCommand(message.Text) {
		if !s.isOwnerAuthorized(message) {
			s.sendCommandReply(ctx, message.Chat.ID, "This command is restricted to the configured owner.")
			return true
		}
		s.executeConnectionsCommand(ctx, message.Chat.ID)
		return true
	}

	if !isSendCommandCandidate(message.Text) {
		return false
	}

	if !s.isOwnerAuthorized(message) {
		s.sendCommandReply(ctx, message.Chat.ID, "This command is restricted to the configured owner.")
		return true
	}

	request, err := parseSendCommand(message.Text)
	if err != nil {
		s.sendCommandReply(ctx, message.Chat.ID, sendCommandUsage)
		return true
	}

	s.executeSendCommand(ctx, message.Chat.ID, request)
	return true
}

func isConnectionsCommand(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "/") {
		return false
	}
	rest := trimmed[1:]
	// allow "/connections", "/connections@bot", "/connections something"
	for i, r := range rest {
		if r == '@' || r == ' ' || r == '\t' {
			rest = rest[:i]
			break
		}
	}
	return strings.EqualFold(rest, "connections")
}

func (s *BusinessMessageService) executeConnectionsCommand(ctx context.Context, responseChatID int64) {
	connections, err := s.repo.ListBusinessConnections(ctx, false)
	if err != nil {
		s.sendCommandReply(ctx, responseChatID, fmt.Sprintf("Failed to list business connections: %s", summarizeCommandError(err)))
		return
	}

	if len(connections) == 0 {
		s.sendCommandReply(ctx, responseChatID, "No business connections recorded yet. Connect the bot to a Telegram Business account to populate this list.")
		return
	}

	lines := []string{fmt.Sprintf("Business connections (%d):", len(connections))}
	for i := range connections {
		c := connections[i]
		username := "<no_username>"
		if c.OwnerUsername != "" {
			username = "@" + c.OwnerUsername
		}
		state := "disabled"
		if c.IsEnabled {
			state = "enabled"
		}
		lines = append(lines, fmt.Sprintf("- %s | owner=%s (id=%d) | %s | can_reply=%t | updated=%s",
			c.ID,
			username,
			c.OwnerUserID,
			state,
			c.CanReply,
			c.UpdatedAt.UTC().Format(time.RFC3339),
		))
	}

	s.sendCommandReply(ctx, responseChatID, strings.Join(lines, "\n"))
}

func (s *BusinessMessageService) isOwnerAuthorized(message *models.Message) bool {
	if message == nil {
		return false
	}

	if message.Chat.ID != s.cfg.OwnerChatID {
		return false
	}

	if message.From != nil && message.From.ID != s.cfg.OwnerChatID {
		return false
	}

	return true
}

func (s *BusinessMessageService) executeSendCommand(ctx context.Context, responseChatID int64, request sendCommandRequest) {
	successRecipients := make([]string, 0, len(request.Recipients))
	unknownRecipients := make([]string, 0)
	failedRecipients := make([]sendCommandFailure, 0)

	for _, usernameKey := range request.Recipients {
		target, err := s.repo.FindLatestChatTargetByUsername(ctx, usernameKey)
		recipientTag := "@" + usernameKey
		if err != nil {
			failedRecipients = append(failedRecipients, sendCommandFailure{
				Recipient: recipientTag,
				Reason:    summarizeCommandError(err),
			})
			continue
		}

		if target == nil {
			unknownRecipients = append(unknownRecipients, recipientTag)
			continue
		}

		if strings.TrimSpace(target.BusinessConnectionID) == "" || target.TargetChatID == 0 {
			failedRecipients = append(failedRecipients, sendCommandFailure{
				Recipient: recipientTag,
				Reason:    "stored target mapping is incomplete",
			})
			continue
		}

		if reason := s.validateBusinessSendConnection(ctx, target.BusinessConnectionID); reason != "" {
			failedRecipients = append(failedRecipients, sendCommandFailure{
				Recipient: recipientTag,
				Reason:    reason,
			})
			s.logger.Warnf("business send blocked recipient=%s business_connection_id=%s reason=%s", recipientTag, target.BusinessConnectionID, reason)
			continue
		}

		_, err = s.tg.SendBusinessMessage(ctx, target.BusinessConnectionID, target.TargetChatID, request.Text)
		if err != nil {
			failedRecipients = append(failedRecipients, sendCommandFailure{
				Recipient: recipientTag,
				Reason:    summarizeCommandError(err),
			})
			s.logger.Warnf("business send failed recipient=%s target_chat_id=%d: %v", recipientTag, target.TargetChatID, err)
			continue
		}

		successRecipients = append(successRecipients, recipientTag)
		s.logger.Infof("business send success recipient=%s target_chat_id=%d", recipientTag, target.TargetChatID)
	}

	s.sendCommandReply(ctx, responseChatID, buildSendSummary(successRecipients, unknownRecipients, failedRecipients))
}

func (s *BusinessMessageService) validateBusinessSendConnection(ctx context.Context, businessConnectionID string) string {
	businessConnectionID = strings.TrimSpace(businessConnectionID)
	if businessConnectionID == "" {
		return "business connection id is empty"
	}

	connection, err := s.repo.GetBusinessConnection(ctx, businessConnectionID)
	if err != nil {
		return "business connection lookup failed: " + summarizeCommandError(err)
	}
	if connection == nil {
		s.logger.Warnf("business send connection is not in registry business_connection_id=%s; attempting send using stored target mapping", businessConnectionID)
		return ""
	}
	if !connection.IsEnabled {
		return "business connection is disabled"
	}
	if !connection.CanReply {
		return "business connection cannot reply (can_reply=false)"
	}

	return ""
}

func (s *BusinessMessageService) sendCommandReply(ctx context.Context, chatID int64, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}

	if _, err := s.tg.SendMessage(ctx, chatID, text); err != nil {
		s.logger.Errorf("send command reply failed chat_id=%d: %v", chatID, err)
	}
}

func parseSendCommand(raw string) (sendCommandRequest, error) {
	args, ok := extractSendCommandArgs(raw)
	if !ok {
		return sendCommandRequest{}, fmt.Errorf("invalid command")
	}

	recipientsRaw, nextPos, ok := readBracketSection(args, 0)
	if !ok {
		return sendCommandRequest{}, fmt.Errorf("invalid recipients block")
	}

	messageRaw, endPos, ok := readBracketSection(args, nextPos)
	if !ok {
		return sendCommandRequest{}, fmt.Errorf("invalid message block")
	}

	if strings.TrimSpace(args[endPos:]) != "" {
		return sendCommandRequest{}, fmt.Errorf("unexpected trailing content")
	}

	recipients, err := parseRecipients(recipientsRaw)
	if err != nil {
		return sendCommandRequest{}, err
	}

	messageText := strings.TrimSpace(messageRaw)
	if messageText == "" {
		return sendCommandRequest{}, fmt.Errorf("empty message")
	}

	return sendCommandRequest{
		Recipients: recipients,
		Text:       messageText,
	}, nil
}

func isSendCommandCandidate(raw string) bool {
	_, ok := extractSendCommandArgs(raw)
	return ok
}

func extractSendCommandArgs(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "/") {
		return "", false
	}

	position := 1
	commandStart := position
	for position < len(trimmed) && isCommandRune(rune(trimmed[position])) {
		position++
	}
	if commandStart == position {
		return "", false
	}

	command := trimmed[commandStart:position]
	if !strings.EqualFold(command, "send") {
		return "", false
	}

	if position < len(trimmed) && trimmed[position] == '@' {
		position++
		mentionStart := position
		for position < len(trimmed) && !unicode.IsSpace(rune(trimmed[position])) && trimmed[position] != '[' {
			position++
		}
		if mentionStart == position {
			return "", false
		}
	}

	if position < len(trimmed) && !unicode.IsSpace(rune(trimmed[position])) && trimmed[position] != '[' {
		return "", false
	}

	return trimmed[position:], true
}

func isCommandRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func readBracketSection(raw string, start int) (string, int, bool) {
	position := start
	for position < len(raw) && unicode.IsSpace(rune(raw[position])) {
		position++
	}
	if position >= len(raw) || raw[position] != '[' {
		return "", 0, false
	}

	closingOffset := strings.IndexByte(raw[position+1:], ']')
	if closingOffset < 0 {
		return "", 0, false
	}

	closingPos := position + 1 + closingOffset
	content := raw[position+1 : closingPos]
	return content, closingPos + 1, true
}

func parseRecipients(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty recipients")
	}

	unique := make(map[string]struct{}, len(parts))
	recipients := make([]string, 0, len(parts))
	for _, part := range parts {
		key := normalizeUsernameKey(part)
		if key == "" {
			return nil, fmt.Errorf("recipient username is empty")
		}
		if !usernameKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid recipient username %q", part)
		}
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		recipients = append(recipients, key)
	}

	if len(recipients) == 0 {
		return nil, fmt.Errorf("empty recipients")
	}

	return recipients, nil
}

func summarizeCommandError(err error) string {
	if err == nil {
		return "unknown error"
	}

	text := compactSingleLine(err.Error())
	if text == "" {
		return "unknown error"
	}

	runes := []rune(text)
	if len(runes) > 180 {
		return string(runes[:177]) + "..."
	}
	return text
}

func buildSendSummary(success []string, unknown []string, failed []sendCommandFailure) string {
	sort.Strings(success)
	sort.Strings(unknown)

	lines := []string{
		"Business send summary",
		fmt.Sprintf("Success: %s", formatListOrNone(success)),
		fmt.Sprintf("Unknown: %s", formatListOrNone(unknown)),
	}

	if len(failed) == 0 {
		lines = append(lines, "Failed: <none>")
		return strings.Join(lines, "\n")
	}

	sort.Slice(failed, func(i, j int) bool {
		return failed[i].Recipient < failed[j].Recipient
	})

	lines = append(lines, "Failed:")
	for _, item := range failed {
		lines = append(lines, fmt.Sprintf("- %s: %s", item.Recipient, item.Reason))
	}
	return strings.Join(lines, "\n")
}

func formatListOrNone(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}

