package service

import (
	"strings"

	"github.com/go-telegram/bot/models"
)

func normalizeUsernameForStorage(value string) string {
	trimmed := strings.TrimSpace(value)
	for strings.HasPrefix(trimmed, "@") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
	}
	return trimmed
}

func normalizeUsernameKey(value string) string {
	return strings.ToLower(normalizeUsernameForStorage(value))
}

func formatUsernameOrPlaceholder(value string) string {
	username := normalizeUsernameForStorage(value)
	if username == "" {
		return "<no_username>"
	}
	return "@" + username
}

func extractSourceUsernameFromMessage(message *models.Message) string {
	if message == nil {
		return ""
	}

	if message.From != nil {
		if username := normalizeUsernameForStorage(message.From.Username); username != "" {
			return username
		}
	}

	return normalizeUsernameForStorage(message.Chat.Username)
}

func extractSourceDisplayNameFromMessage(message *models.Message) string {
	if message == nil {
		return ""
	}

	if message.From != nil {
		if displayName := buildDisplayNameFromUser(message.From); displayName != "" {
			return displayName
		}
	}

	return buildDisplayNameFromChat(message.Chat)
}

func buildDisplayNameFromUser(user *models.User) string {
	if user == nil {
		return ""
	}

	fullName := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if fullName != "" {
		return compactSingleLine(fullName)
	}

	if username := normalizeUsernameForStorage(user.Username); username != "" {
		return "@" + username
	}

	return ""
}

func buildDisplayNameFromChat(chat models.Chat) string {
	fullName := strings.TrimSpace(strings.TrimSpace(chat.FirstName) + " " + strings.TrimSpace(chat.LastName))
	if fullName != "" {
		return compactSingleLine(fullName)
	}

	if title := compactSingleLine(chat.Title); title != "" {
		return title
	}

	if username := normalizeUsernameForStorage(chat.Username); username != "" {
		return "@" + username
	}

	return ""
}

func compactSingleLine(value string) string {
	if value == "" {
		return ""
	}
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

