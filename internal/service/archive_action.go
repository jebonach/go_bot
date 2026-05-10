package service

import (
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"
)

const (
	telegramMessageLimit = 4096
	telegramCaptionLimit = 1024
)

type archiveSendMethod string

const (
	archiveSendMethodUnsupported archiveSendMethod = "unsupported"
	archiveSendMethodText        archiveSendMethod = "send_message"
	archiveSendMethodPhoto       archiveSendMethod = "send_photo"
	archiveSendMethodVoice       archiveSendMethod = "send_voice"
	archiveSendMethodAudio       archiveSendMethod = "send_audio"
	archiveSendMethodDocument    archiveSendMethod = "send_document"
	archiveSendMethodVideo       archiveSendMethod = "send_video"
	archiveSendMethodAnimation   archiveSendMethod = "send_animation"
	archiveSendMethodSticker     archiveSendMethod = "send_sticker"
	archiveSendMethodVideoNote   archiveSendMethod = "send_video_note"
)

type archiveSendResult struct {
	MessageID         int
	MetadataMessageID *int
}

type archiveAction struct {
	contentType  string
	method       archiveSendMethod
	fileID       string
	text         string
	caption      string
	metadataText string
}

func buildArchiveAction(message *models.Message, classification Classification) (archiveAction, bool) {
	return buildArchiveActionWithVersion(message, classification, 0)
}

func buildArchiveActionWithVersion(message *models.Message, classification Classification, versionNo int) (archiveAction, bool) {
	if message == nil {
		return archiveAction{}, false
	}

	header := buildArchiveHeader(message, classification.ContentType, versionNo)
	switch classification.ContentType {
	case ContentTypeText:
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodText,
			text:        joinWithBody(header, message.Text, telegramMessageLimit),
		}, true
	case ContentTypePhoto:
		fileID := classification.FileID
		if fileID == "" && len(message.Photo) > 0 {
			fileID = message.Photo[len(message.Photo)-1].FileID
		}
		if fileID == "" {
			return archiveAction{}, false
		}
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodPhoto,
			fileID:      fileID,
			caption:     joinWithBody(header, message.Caption, telegramCaptionLimit),
		}, true
	case ContentTypeVoice:
		if classification.FileID == "" {
			return archiveAction{}, false
		}
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodVoice,
			fileID:      classification.FileID,
			caption:     joinWithBody(header, message.Caption, telegramCaptionLimit),
		}, true
	case ContentTypeAudio:
		if classification.FileID == "" {
			return archiveAction{}, false
		}
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodAudio,
			fileID:      classification.FileID,
			caption:     joinWithBody(header, message.Caption, telegramCaptionLimit),
		}, true
	case ContentTypeDocument:
		if classification.FileID == "" {
			return archiveAction{}, false
		}
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodDocument,
			fileID:      classification.FileID,
			caption:     joinWithBody(header, message.Caption, telegramCaptionLimit),
		}, true
	case ContentTypeVideo:
		if classification.FileID == "" {
			return archiveAction{}, false
		}
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodVideo,
			fileID:      classification.FileID,
			caption:     joinWithBody(header, message.Caption, telegramCaptionLimit),
		}, true
	case ContentTypeAnimation:
		if classification.FileID == "" {
			return archiveAction{}, false
		}
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodAnimation,
			fileID:      classification.FileID,
			caption:     joinWithBody(header, message.Caption, telegramCaptionLimit),
		}, true
	case ContentTypeSticker:
		if classification.FileID == "" {
			return archiveAction{}, false
		}
		return archiveAction{
			contentType:  classification.ContentType,
			method:       archiveSendMethodSticker,
			fileID:       classification.FileID,
			metadataText: joinWithBody(header, classification.MetadataJSON, telegramMessageLimit),
		}, true
	case ContentTypeVideoNote:
		if classification.FileID == "" {
			return archiveAction{}, false
		}
		return archiveAction{
			contentType:  classification.ContentType,
			method:       archiveSendMethodVideoNote,
			fileID:       classification.FileID,
			metadataText: joinWithBody(header, classification.MetadataJSON, telegramMessageLimit),
		}, true
	case ContentTypeContact, ContentTypeLocation, ContentTypeVenue, ContentTypePoll, ContentTypeDice:
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodText,
			text:        joinWithBody(header, classification.MetadataJSON, telegramMessageLimit),
		}, true
	default:
		if classification.Archivable && classification.MetadataJSON != "" {
			return archiveAction{
				contentType: classification.ContentType,
				method:      archiveSendMethodText,
				text:        joinWithBody(header, classification.MetadataJSON, telegramMessageLimit),
			}, true
		}
		return archiveAction{
			contentType: classification.ContentType,
			method:      archiveSendMethodUnsupported,
		}, false
	}
}

func buildArchiveHeader(message *models.Message, contentType string, versionNo int) string {
	if message == nil {
		if versionNo > 0 {
			return fmt.Sprintf("[ARCHIVE]\nversion: %d\ntype: %s", versionNo, contentType)
		}
		return fmt.Sprintf("[ARCHIVE]\ntype: %s", contentType)
	}

	lines := []string{"[ARCHIVE]"}
	if versionNo > 0 {
		lines = append(lines, fmt.Sprintf("version: %d", versionNo))
	}

	lines = append(lines,
		fmt.Sprintf("type: %s", contentType),
		fmt.Sprintf("source_chat_id: %d", message.Chat.ID),
		fmt.Sprintf("source_message_id: %d", message.ID),
		fmt.Sprintf("source_username: %s", formatUsernameOrPlaceholder(extractSourceUsernameFromMessage(message))),
	)

	if displayName := compactSingleLine(extractSourceDisplayNameFromMessage(message)); displayName != "" {
		lines = append(lines, fmt.Sprintf("source_display_name: %s", displayName))
	}
	if message.From != nil {
		lines = append(lines, fmt.Sprintf("source_from_id: %d", message.From.ID))
	}

	return strings.Join(lines, "\n")
}

func joinWithBody(header string, body string, limit int) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return clampText(header, limit)
	}
	return clampText(header+"\n\n"+body, limit)
}

func clampText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}
