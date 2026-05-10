package service

import (
	"time"

	"removed-messages/internal/config"
)

func calculateExpiresAt(now time.Time, cfg *config.Config, contentType string) time.Time {
	switch contentType {
	case ContentTypeVoice, ContentTypeAudio:
		return now.Add(cfg.RetentionAudio)
	case ContentTypePhoto:
		return now.Add(cfg.RetentionPhoto)
	case ContentTypeText:
		return now.Add(cfg.RetentionText)
	case ContentTypeDocument, ContentTypeVideo, ContentTypeAnimation, ContentTypeSticker, ContentTypeVideoNote,
		ContentTypeContact, ContentTypeLocation, ContentTypeVenue, ContentTypePoll, ContentTypeDice, ContentTypeUnknown:
		return now.Add(cfg.RetentionOtherMedia)
	default:
		return now.Add(cfg.RetentionOtherMedia)
	}
}
