package service

import (
	"encoding/json"
	"strings"

	"github.com/go-telegram/bot/models"
)

const (
	MessageKindMedia   = "media"
	MessageKindService = "service"
	MessageKindText    = "text"
	MessageKindUnknown = "unknown"

	ContentTypeAnimation                       = "animation"
	ContentTypeAudio                           = "audio"
	ContentTypeBoostAdded                      = "boost_added"
	ContentTypeChannelChatCreated              = "channel_chat_created"
	ContentTypeChatBackgroundSet               = "chat_background_set"
	ContentTypeChatShared                      = "chat_shared"
	ContentTypeChecklist                       = "checklist"
	ContentTypeChecklistTasksAdded             = "checklist_tasks_added"
	ContentTypeChecklistTasksDone              = "checklist_tasks_done"
	ContentTypeConnectedWebsite                = "connected_website"
	ContentTypeContact                         = "contact"
	ContentTypeDice                            = "dice"
	ContentTypeDeleteChatPhoto                 = "delete_chat_photo"
	ContentTypeDirectMessagePriceChanged       = "direct_message_price_changed"
	ContentTypeDocument                        = "document"
	ContentTypeForumTopicClosed                = "forum_topic_closed"
	ContentTypeForumTopicCreated               = "forum_topic_created"
	ContentTypeForumTopicEdited                = "forum_topic_edited"
	ContentTypeForumTopicReopened              = "forum_topic_reopened"
	ContentTypeGame                            = "game"
	ContentTypeGeneralForumTopicHidden         = "general_forum_topic_hidden"
	ContentTypeGeneralForumTopicUnhidden       = "general_forum_topic_unhidden"
	ContentTypeGift                            = "gift"
	ContentTypeGiftUpgradeSent                 = "gift_upgrade_sent"
	ContentTypeGiveaway                        = "giveaway"
	ContentTypeGiveawayCompleted               = "giveaway_completed"
	ContentTypeGiveawayCreated                 = "giveaway_created"
	ContentTypeGiveawayWinners                 = "giveaway_winners"
	ContentTypeGroupChatCreated                = "group_chat_created"
	ContentTypeInvoice                         = "invoice"
	ContentTypeLeftChatMember                  = "left_chat_member"
	ContentTypeLocation                        = "location"
	ContentTypeMessageAutoDeleteTimerChanged   = "message_auto_delete_timer_changed"
	ContentTypeMigrateFromChatID               = "migrate_from_chat_id"
	ContentTypeMigrateToChatID                 = "migrate_to_chat_id"
	ContentTypeNewChatMembers                  = "new_chat_members"
	ContentTypeNewChatPhoto                    = "new_chat_photo"
	ContentTypeNewChatTitle                    = "new_chat_title"
	ContentTypePaidMedia                       = "paid_media"
	ContentTypePaidMessagePriceChanged         = "paid_message_price_changed"
	ContentTypePassportData                    = "passport_data"
	ContentTypePhoto                           = "photo"
	ContentTypePinnedMessage                   = "pinned_message"
	ContentTypePoll                            = "poll"
	ContentTypeProximityAlertTriggered         = "proximity_alert_triggered"
	ContentTypeRefundedPayment                 = "refunded_payment"
	ContentTypeSticker                         = "sticker"
	ContentTypeStory                           = "story"
	ContentTypeSuccessfulPayment               = "successful_payment"
	ContentTypeSuggestedPostApprovalFailed     = "suggested_post_approval_failed"
	ContentTypeSuggestedPostApproved           = "suggested_post_approved"
	ContentTypeSuggestedPostDeclined           = "suggested_post_declined"
	ContentTypeSuggestedPostInfo               = "suggested_post_info"
	ContentTypeSuggestedPostPaid               = "suggested_post_paid"
	ContentTypeSuggestedPostRefunded           = "suggested_post_refunded"
	ContentTypeSupergroupChatCreated           = "supergroup_chat_created"
	ContentTypeText                            = "text"
	ContentTypeUniqueGift                      = "unique_gift"
	ContentTypeUnknown                         = "unknown"
	ContentTypeUsersShared                     = "users_shared"
	ContentTypeVenue                           = "venue"
	ContentTypeVideo                           = "video"
	ContentTypeVideoNote                       = "video_note"
	ContentTypeVoice                           = "voice"
	ContentTypeVoiceChatEnded                  = "voice_chat_ended"
	ContentTypeVoiceChatParticipantsInvited    = "voice_chat_participants_invited"
	ContentTypeVoiceChatScheduled              = "voice_chat_scheduled"
	ContentTypeVoiceChatStarted                = "voice_chat_started"
	ContentTypeWebAppData                      = "web_app_data"
	ContentTypeWriteAccessAllowed              = "write_access_allowed"

	metadataPreviewLimit = 3500
)

type Classification struct {
	MessageKind    string
	ContentType    string
	FileID         string
	FileUniqueID   string
	MetadataJSON    string
	Archivable     bool
}

func ClassifyMessage(message *models.Message) Classification {
	if message == nil {
		return Classification{
			MessageKind: MessageKindUnknown,
			ContentType: ContentTypeUnknown,
		}
	}
	switch {
	case message.Voice != nil:
		return Classification{
			MessageKind:    MessageKindMedia,
			ContentType:    ContentTypeVoice,
			FileID:         message.Voice.FileID,
			FileUniqueID:   message.Voice.FileUniqueID,
			MetadataJSON:    compactJSON(message.Voice),
			Archivable:     true,
		}
	case message.Audio != nil:
		return Classification{
			MessageKind:    MessageKindMedia,
			ContentType:    ContentTypeAudio,
			FileID:         message.Audio.FileID,
			FileUniqueID:   message.Audio.FileUniqueID,
			MetadataJSON:    compactJSON(message.Audio),
			Archivable:     true,
		}
	case len(message.Photo) > 0:
		largest := message.Photo[len(message.Photo)-1]
		return Classification{
			MessageKind:    MessageKindMedia,
			ContentType:    ContentTypePhoto,
			FileID:         largest.FileID,
			FileUniqueID:   largest.FileUniqueID,
			MetadataJSON:    compactJSON(largest),
			Archivable:     true,
		}
	case message.Video != nil:
		return Classification{
			MessageKind:    MessageKindMedia,
			ContentType:    ContentTypeVideo,
			FileID:         message.Video.FileID,
			FileUniqueID:   message.Video.FileUniqueID,
			MetadataJSON:    compactJSON(message.Video),
			Archivable:     true,
		}
	case message.Animation != nil:
		return Classification{
			MessageKind:    MessageKindMedia,
			ContentType:    ContentTypeAnimation,
			FileID:         message.Animation.FileID,
			FileUniqueID:   message.Animation.FileUniqueID,
			MetadataJSON:    compactJSON(message.Animation),
			Archivable:     true,
		}
	case message.Sticker != nil:
		return Classification{
			MessageKind:  MessageKindMedia,
			ContentType:  ContentTypeSticker,
			FileID:       message.Sticker.FileID,
			FileUniqueID: message.Sticker.FileUniqueID,
			MetadataJSON:  compactJSON(message.Sticker),
			Archivable:   true,
		}
	case message.Text != "":
		return Classification{
			MessageKind: MessageKindText,
			ContentType: ContentTypeText,
			Archivable:  true,
		}
	case message.Document != nil:
		return Classification{
			MessageKind:    MessageKindMedia,
			ContentType:    ContentTypeDocument,
			FileID:         message.Document.FileID,
			FileUniqueID:   message.Document.FileUniqueID,
			MetadataJSON:    compactJSON(message.Document),
			Archivable:     true,
		}
	case message.VideoNote != nil:
		return Classification{
			MessageKind:    MessageKindMedia,
			ContentType:    ContentTypeVideoNote,
			FileID:         message.VideoNote.FileID,
			FileUniqueID:   message.VideoNote.FileUniqueID,
			MetadataJSON:    compactJSON(message.VideoNote),
			Archivable:     true,
		}
	case message.Contact != nil:
		return Classification{
			MessageKind:    MessageKindMedia,
			ContentType:    ContentTypeContact,
			MetadataJSON:   compactJSON(message.Contact),
			Archivable:     true,
		}
	case message.Location != nil:
		return Classification{
			MessageKind:  MessageKindMedia,
			ContentType:  ContentTypeLocation,
			MetadataJSON: compactJSON(message.Location),
			Archivable:   true,
		}
	case message.Venue != nil:
		return Classification{
			MessageKind:  MessageKindMedia,
			ContentType:  ContentTypeVenue,
			MetadataJSON: compactJSON(message.Venue),
			Archivable:   true,
		}
	case message.Poll != nil:
		return Classification{
			MessageKind:  MessageKindMedia,
			ContentType:  ContentTypePoll,
			MetadataJSON: compactJSON(message.Poll),
			Archivable:   true,
		}
	case message.Dice != nil:
		return Classification{
			MessageKind:  MessageKindMedia,
			ContentType:  ContentTypeDice,
			MetadataJSON: compactJSON(message.Dice),
			Archivable:   true,
		}
	case message.SuggestedPostInfo != nil:
		return metadataClassification(MessageKindService, ContentTypeSuggestedPostInfo, message.SuggestedPostInfo)
	case message.PaidMedia != nil:
		return metadataClassification(MessageKindMedia, ContentTypePaidMedia, message.PaidMedia)
	case message.Story != nil:
		return metadataClassification(MessageKindMedia, ContentTypeStory, message.Story)
	case message.Checklist != nil:
		return metadataClassification(MessageKindMedia, ContentTypeChecklist, message.Checklist)
	case message.Game != nil:
		return metadataClassification(MessageKindMedia, ContentTypeGame, message.Game)
	case len(message.NewChatMembers) > 0:
		return metadataClassification(MessageKindService, ContentTypeNewChatMembers, message.NewChatMembers)
	case message.LeftChatMember != nil:
		return metadataClassification(MessageKindService, ContentTypeLeftChatMember, message.LeftChatMember)
	case message.NewChatTitle != "":
		return metadataClassification(MessageKindService, ContentTypeNewChatTitle, map[string]string{"new_chat_title": message.NewChatTitle})
	case len(message.NewChatPhoto) > 0:
		return metadataClassification(MessageKindService, ContentTypeNewChatPhoto, message.NewChatPhoto)
	case message.DeleteChatPhoto:
		return metadataClassification(MessageKindService, ContentTypeDeleteChatPhoto, map[string]bool{"delete_chat_photo": true})
	case message.GroupChatCreated:
		return metadataClassification(MessageKindService, ContentTypeGroupChatCreated, map[string]bool{"group_chat_created": true})
	case message.SupergroupChatCreated:
		return metadataClassification(MessageKindService, ContentTypeSupergroupChatCreated, map[string]bool{"supergroup_chat_created": true})
	case message.ChannelChatCreated:
		return metadataClassification(MessageKindService, ContentTypeChannelChatCreated, map[string]bool{"channel_chat_created": true})
	case message.MessageAutoDeleteTimerChanged != nil:
		return metadataClassification(MessageKindService, ContentTypeMessageAutoDeleteTimerChanged, message.MessageAutoDeleteTimerChanged)
	case message.MigrateToChatID != 0:
		return metadataClassification(MessageKindService, ContentTypeMigrateToChatID, map[string]int64{"migrate_to_chat_id": message.MigrateToChatID})
	case message.MigrateFromChatID != 0:
		return metadataClassification(MessageKindService, ContentTypeMigrateFromChatID, map[string]int64{"migrate_from_chat_id": message.MigrateFromChatID})
	case message.PinnedMessage != nil:
		return metadataClassification(MessageKindService, ContentTypePinnedMessage, message.PinnedMessage)
	case message.Invoice != nil:
		return metadataClassification(MessageKindService, ContentTypeInvoice, message.Invoice)
	case message.SuccessfulPayment != nil:
		return metadataClassification(MessageKindService, ContentTypeSuccessfulPayment, message.SuccessfulPayment)
	case message.RefundedPayment != nil:
		return metadataClassification(MessageKindService, ContentTypeRefundedPayment, message.RefundedPayment)
	case message.UsersShared != nil:
		return metadataClassification(MessageKindService, ContentTypeUsersShared, message.UsersShared)
	case message.ChatShared != nil:
		return metadataClassification(MessageKindService, ContentTypeChatShared, message.ChatShared)
	case message.Gift != nil:
		return metadataClassification(MessageKindService, ContentTypeGift, message.Gift)
	case message.UniqueGift != nil:
		return metadataClassification(MessageKindService, ContentTypeUniqueGift, message.UniqueGift)
	case message.GiftUpgradeSent != nil:
		return metadataClassification(MessageKindService, ContentTypeGiftUpgradeSent, message.GiftUpgradeSent)
	case message.ConnectedWebsite != "":
		return metadataClassification(MessageKindService, ContentTypeConnectedWebsite, map[string]string{"connected_website": message.ConnectedWebsite})
	case message.WriteAccessAllowed != nil:
		return metadataClassification(MessageKindService, ContentTypeWriteAccessAllowed, message.WriteAccessAllowed)
	case message.PassportData != nil:
		return metadataClassification(MessageKindService, ContentTypePassportData, message.PassportData)
	case message.ProximityAlertTriggered != nil:
		return metadataClassification(MessageKindService, ContentTypeProximityAlertTriggered, message.ProximityAlertTriggered)
	case message.BoostAdded != nil:
		return metadataClassification(MessageKindService, ContentTypeBoostAdded, message.BoostAdded)
	case message.ChatBackgroundSet != nil:
		return metadataClassification(MessageKindService, ContentTypeChatBackgroundSet, message.ChatBackgroundSet)
	case message.ChecklistTasksDone != nil:
		return metadataClassification(MessageKindService, ContentTypeChecklistTasksDone, message.ChecklistTasksDone)
	case message.ChecklistTasksAdded != nil:
		return metadataClassification(MessageKindService, ContentTypeChecklistTasksAdded, message.ChecklistTasksAdded)
	case message.DirectMessagePriceChanged != nil:
		return metadataClassification(MessageKindService, ContentTypeDirectMessagePriceChanged, message.DirectMessagePriceChanged)
	case message.ForumTopicCreated != nil:
		return metadataClassification(MessageKindService, ContentTypeForumTopicCreated, message.ForumTopicCreated)
	case message.ForumTopicEdited != nil:
		return metadataClassification(MessageKindService, ContentTypeForumTopicEdited, message.ForumTopicEdited)
	case message.ForumTopicClosed != nil:
		return metadataClassification(MessageKindService, ContentTypeForumTopicClosed, message.ForumTopicClosed)
	case message.ForumTopicReopened != nil:
		return metadataClassification(MessageKindService, ContentTypeForumTopicReopened, message.ForumTopicReopened)
	case message.GeneralForumTopicHidden != nil:
		return metadataClassification(MessageKindService, ContentTypeGeneralForumTopicHidden, message.GeneralForumTopicHidden)
	case message.GeneralForumTopicUnhidden != nil:
		return metadataClassification(MessageKindService, ContentTypeGeneralForumTopicUnhidden, message.GeneralForumTopicUnhidden)
	case message.GiveawayCreated != nil:
		return metadataClassification(MessageKindService, ContentTypeGiveawayCreated, message.GiveawayCreated)
	case message.Giveaway != nil:
		return metadataClassification(MessageKindService, ContentTypeGiveaway, message.Giveaway)
	case message.GiveawayWinners != nil:
		return metadataClassification(MessageKindService, ContentTypeGiveawayWinners, message.GiveawayWinners)
	case message.GiveawayCompleted != nil:
		return metadataClassification(MessageKindService, ContentTypeGiveawayCompleted, message.GiveawayCompleted)
	case message.PaidMessagePriceChanged != nil:
		return metadataClassification(MessageKindService, ContentTypePaidMessagePriceChanged, message.PaidMessagePriceChanged)
	case message.SuggestedPostApproved != nil:
		return metadataClassification(MessageKindService, ContentTypeSuggestedPostApproved, message.SuggestedPostApproved)
	case message.SuggestedPostApprovalFailed != nil:
		return metadataClassification(MessageKindService, ContentTypeSuggestedPostApprovalFailed, message.SuggestedPostApprovalFailed)
	case message.SuggestedPostDeclined != nil:
		return metadataClassification(MessageKindService, ContentTypeSuggestedPostDeclined, message.SuggestedPostDeclined)
	case message.SuggestedPostPaid != nil:
		return metadataClassification(MessageKindService, ContentTypeSuggestedPostPaid, message.SuggestedPostPaid)
	case message.SuggestedPostRefunded != nil:
		return metadataClassification(MessageKindService, ContentTypeSuggestedPostRefunded, message.SuggestedPostRefunded)
	case message.VoiceChatScheduled != nil:
		return metadataClassification(MessageKindService, ContentTypeVoiceChatScheduled, message.VoiceChatScheduled)
	case message.VoiceChatStarted != nil:
		return metadataClassification(MessageKindService, ContentTypeVoiceChatStarted, message.VoiceChatStarted)
	case message.VoiceChatEnded != nil:
		return metadataClassification(MessageKindService, ContentTypeVoiceChatEnded, message.VoiceChatEnded)
	case message.VoiceChatParticipantsInvited != nil:
		return metadataClassification(MessageKindService, ContentTypeVoiceChatParticipantsInvited, message.VoiceChatParticipantsInvited)
	case message.WebAppData != nil:
		return metadataClassification(MessageKindService, ContentTypeWebAppData, message.WebAppData)
	default:
		return Classification{
			MessageKind:    MessageKindUnknown,
			ContentType:    ContentTypeUnknown,
			MetadataJSON:    compactUnknownMessageMetadata(message),
			Archivable:     true,
		}
	}
}

func metadataClassification(messageKind string, contentType string, metadata any) Classification {
	return Classification{
		MessageKind:  messageKind,
		ContentType:  contentType,
		MetadataJSON: compactJSON(metadata),
		Archivable:   true,
	}
}

func truncatePreview(value string, maxChars int) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if maxChars <= 0 || normalized == "" {
		return normalized
	}

	runes := []rune(normalized)
	if len(runes) <= maxChars {
		return normalized
	}

	return string(runes[:maxChars]) + "..."
}

func compactJSON(value any) string {
	if value == nil {
		return ""
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return truncatePreview(string(raw), metadataPreviewLimit)
}

func compactUnknownMessageMetadata(message *models.Message) string {
	if message == nil {
		return ""
	}

	payload := map[string]any{
		"message_id":            message.ID,
		"date":                  message.Date,
		"chat_id":               message.Chat.ID,
		"edit_date":             message.EditDate,
		"media_group_id":        message.MediaGroupID,
		"has_protected_content": message.HasProtectedContent,
		"is_paid_post":          message.IsPaidPost,
	}
	if message.From != nil {
		payload["from_id"] = message.From.ID
	}
	if message.SenderChat != nil {
		payload["sender_chat_id"] = message.SenderChat.ID
	}

	return compactJSON(payload)
}
