package service

import (
	"context"
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"removed-messages/internal/config"
	"removed-messages/internal/logging"
	"removed-messages/internal/storage"
)

type TelegramClient interface {
	ArchiveSender
	OwnerNotifier
	BusinessSender
	ErrorClassifier
}

type ArchiveSender interface {
	CopyMessage(ctx context.Context, fromChatID int64, messageID int, toChatID int64) (int, error)
	DeleteMessage(ctx context.Context, chatID int64, messageID int) error
	SendMessage(ctx context.Context, chatID int64, text string) (int, error)
	SendPhotoByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error)
	SendVoiceByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error)
	SendAudioByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error)
	SendDocumentByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error)
	SendVideoByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error)
	SendAnimationByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error)
	SendStickerByFileID(ctx context.Context, chatID int64, fileID string) (int, error)
	SendVideoNoteByFileID(ctx context.Context, chatID int64, fileID string) (int, error)
}

type OwnerNotifier interface {
	SendOwnerNotification(ctx context.Context, ownerChatID int64, text string) error
}

type BusinessSender interface {
	SendBusinessMessage(ctx context.Context, businessConnectionID string, chatID int64, text string) (int, error)
}

type ErrorClassifier interface {
	IsMissingMessageError(err error) bool
}

type deleteKey struct {
	businessConnectionID string
	sourceChatID         int64
	sourceMessageID      int
}

type deleteProcessResult string

const (
	deleteProcessProcessed        deleteProcessResult = "processed"
	deleteProcessAlreadyProcessed deleteProcessResult = "already_processed"
	deleteProcessMissingMessage   deleteProcessResult = "missing_message"
)

const (
	archiveCopyStatusPending = "pending"
	archiveCopyStatusSent    = "sent"
	archiveCopyStatusFailed  = "failed"
)

type BusinessMessageService struct {
	cfg    *config.Config
	repo   storage.Repository
	tg     TelegramClient
	logger *logging.Logger

	pendingDeleteRetryDelay time.Duration
	pendingDeleteMaxAge     time.Duration
	pendingDeleteSweepTick  time.Duration

	mediaGroupFlushDelay time.Duration
	mediaGroupSweepTick  time.Duration

	mediaGroupMu     sync.Mutex
	mediaGroupBuffer map[string]*mediaGroupBucket
}

type mediaGroupBucket struct {
	BusinessConnectionID string
	SourceChatID         int64
	OwnerChatID          int64
	MediaGroupID         string
	FirstSeenAt          time.Time
	LastSeenAt           time.Time
	SourceMessageIDs     []int
}

func NewBusinessMessageService(cfg *config.Config, repo storage.Repository, tg TelegramClient, logger *logging.Logger) *BusinessMessageService {
	return &BusinessMessageService{
		cfg:                     cfg,
		repo:                    repo,
		tg:                      tg,
		logger:                  logger,
		pendingDeleteRetryDelay: 2 * time.Second,
		pendingDeleteMaxAge:     90 * time.Second,
		pendingDeleteSweepTick:  2 * time.Second,
		mediaGroupFlushDelay:    1500 * time.Millisecond,
		mediaGroupSweepTick:     500 * time.Millisecond,
		mediaGroupBuffer:        make(map[string]*mediaGroupBucket),
	}
}

func (s *BusinessMessageService) HandleBusinessMessage(ctx context.Context, message *models.Message) {
	if message == nil {
		return
	}
	if message.BusinessConnectionID == "" {
		s.logger.Warnf("skip business message without business_connection_id source_chat_id=%d source_message_id=%d", message.Chat.ID, message.ID)
		return
	}

	if !s.isBusinessConnectionActive(ctx, message.BusinessConnectionID) {
		s.logger.Warnf("business message arrived on disabled connection business_connection_id=%s source_chat_id=%d source_message_id=%d", message.BusinessConnectionID, message.Chat.ID, message.ID)
	}

	classification := ClassifyMessage(message)
	s.logger.Infof("incoming business message parsed source_chat_id=%d source_message_id=%d content_type=%s message_kind=%s media_group_id=%s", message.Chat.ID, message.ID, classification.ContentType, classification.MessageKind, message.MediaGroupID)

	now := time.Now().UTC()
	createdAt := now
	if message.Date > 0 {
		createdAt = time.Unix(int64(message.Date), 0).UTC()
	}

	record := &storage.ArchivedMessage{
		BusinessConnectionID: message.BusinessConnectionID,
		SourceChatID:         message.Chat.ID,
		SourceMessageID:      message.ID,
		SourceUsername:       extractSourceUsernameFromMessage(message),
		SourceDisplayName:    extractSourceDisplayNameFromMessage(message),
		MediaGroupID:         message.MediaGroupID,
		OwnerChatID:          s.cfg.OwnerChatID,
		MessageKind:          classification.MessageKind,
		ContentType:          classification.ContentType,
		CreatedAt:            createdAt,
		UpdatedAt:            now,
		ExpiresAt:            calculateExpiresAt(now, s.cfg, classification.ContentType),
	}
	if message.From != nil {
		sourceFromID := message.From.ID
		record.SourceFromID = &sourceFromID
	}

	s.upsertBusinessTarget(ctx, record, now)

	inserted, err := s.repo.InsertIfNotExists(ctx, record)
	if err != nil {
		s.logger.Errorf("db insert failed source_chat_id=%d source_message_id=%d content_type=%s: %v", message.Chat.ID, message.ID, classification.ContentType, err)
		return
	}
	if !inserted {
		s.logger.Debugf("duplicate business message ignored source_chat_id=%d source_message_id=%d", message.Chat.ID, message.ID)
		return
	}
	s.logger.Infof("db insert success source_chat_id=%d source_message_id=%d content_type=%s", message.Chat.ID, message.ID, classification.ContentType)

	parent, err := s.repo.GetBySource(ctx, message.BusinessConnectionID, message.Chat.ID, message.ID)
	if err != nil {
		s.logger.Errorf("load parent after insert failed source_chat_id=%d source_message_id=%d: %v", message.Chat.ID, message.ID, err)
		return
	}
	if parent == nil {
		s.logger.Errorf("parent not found after insert source_chat_id=%d source_message_id=%d", message.Chat.ID, message.ID)
		return
	}

	key := deleteKey{
		businessConnectionID: message.BusinessConnectionID,
		sourceChatID:         message.Chat.ID,
		sourceMessageID:      message.ID,
	}
	s.reconcilePendingDeleteIfExists(ctx, key, "message_stored")

	s.archiveAndPersistVersion(ctx, parent, message, classification, 1, false)

	if message.MediaGroupID != "" {
		s.trackMediaGroup(message)
	}
}

func (s *BusinessMessageService) HandleEditedBusinessMessage(ctx context.Context, message *models.Message) {
	if message == nil {
		return
	}
	if message.BusinessConnectionID == "" {
		s.logger.Warnf("skip edited business message without business_connection_id source_chat_id=%d source_message_id=%d", message.Chat.ID, message.ID)
		return
	}

	parent, err := s.repo.GetBySource(ctx, message.BusinessConnectionID, message.Chat.ID, message.ID)
	if err != nil {
		s.logger.Errorf("load parent for edit source_chat_id=%d source_message_id=%d: %v", message.Chat.ID, message.ID, err)
		return
	}
	if parent == nil {
		s.logger.Infof("edited business message not found in store source_chat_id=%d source_message_id=%d", message.Chat.ID, message.ID)
		return
	}

	oldVersion, err := s.ensureLatestVersion(ctx, parent)
	if err != nil {
		s.logger.Errorf("load latest version for edit source_chat_id=%d source_message_id=%d: %v", message.Chat.ID, message.ID, err)
		return
	}
	if oldVersion == nil {
		s.logger.Errorf("latest version is nil source_chat_id=%d source_message_id=%d", message.Chat.ID, message.ID)
		return
	}

	if message.EditDate > 0 && oldVersion.EditDate != nil && *oldVersion.EditDate == int64(message.EditDate) {
		s.logger.Infof("duplicate edit ignored source_chat_id=%d source_message_id=%d edit_date=%d", message.Chat.ID, message.ID, message.EditDate)
		return
	}

	classification := ClassifyMessage(message)
	s.logger.Infof("edit detected source_chat_id=%d source_message_id=%d old_version_no=%d new_content_type=%s", message.Chat.ID, message.ID, oldVersion.VersionNo, classification.ContentType)

	newVersion, archiveMessageID := s.archiveAndPersistVersion(ctx, parent, message, classification, 0, true)
	if newVersion == nil {
		return
	}

	var archiveChatID *int64
	if archiveMessageID != nil {
		v := s.cfg.ArchiveChatID
		archiveChatID = &v
	}

	if err := s.repo.UpdateCurrentFromVersion(
		ctx,
		message.BusinessConnectionID,
		message.Chat.ID,
		message.ID,
		classification.MessageKind,
		classification.ContentType,
		archiveChatID,
		archiveMessageID,
		time.Now().UTC(),
	); err != nil {
		s.logger.Errorf("update current message from version failed source_chat_id=%d source_message_id=%d version_no=%d: %v", message.Chat.ID, message.ID, newVersion.VersionNo, err)
	}

	mediaChanged := oldVersion.ContentType != newVersion.ContentType
	if err := s.sendEditNotification(ctx, parent, oldVersion, newVersion, mediaChanged); err != nil {
		s.logger.Errorf("owner edit notification failed source_chat_id=%d source_message_id=%d version_no=%d: %v", message.Chat.ID, message.ID, newVersion.VersionNo, err)
		return
	}
	s.logger.Infof("owner edit notification sent source_chat_id=%d source_message_id=%d version_no=%d", message.Chat.ID, message.ID, newVersion.VersionNo)
}

// archiveAndPersistVersion creates a version row and pushes content to the archive chat using the
// outbox pattern: a pending archive_copies row is written before sending so a crash mid-flight is
// detectable. For edits use atomicNextVersion=true; the version_no is then computed atomically by SQL.
func (s *BusinessMessageService) archiveAndPersistVersion(
	ctx context.Context,
	parent *storage.ArchivedMessage,
	message *models.Message,
	classification Classification,
	explicitVersionNo int,
	atomicNextVersion bool,
) (*storage.MessageVersion, *int) {
	var editDate *int64
	if message.EditDate > 0 {
		v := int64(message.EditDate)
		editDate = &v
	}

	createdAt := time.Now().UTC()

	version := &storage.MessageVersion{
		ParentMessageID: parent.ID,
		ContentType:     classification.ContentType,
		EditDate:        editDate,
		CreatedAt:       createdAt,
	}

	var (
		insertedID int64
		err        error
		versionNo  int
	)

	if atomicNextVersion {
		insertedID, versionNo, err = s.repo.InsertNextVersion(ctx, version)
	} else {
		version.VersionNo = explicitVersionNo
		versionNo = explicitVersionNo
		insertedID, err = s.repo.InsertVersion(ctx, version)
	}
	if err != nil {
		s.logger.Errorf("insert version failed source_chat_id=%d source_message_id=%d: %v", message.Chat.ID, message.ID, err)
		return nil, nil
	}
	version.ID = insertedID
	version.VersionNo = versionNo

	action, ok := buildArchiveActionWithVersion(message, classification, versionNo)
	if !ok || action.method == archiveSendMethodUnsupported {
		s.logger.Warnf("unsupported content type fallback source_chat_id=%d source_message_id=%d content_type=%s version_no=%d", message.Chat.ID, message.ID, classification.ContentType, versionNo)
		return version, nil
	}

	pendingCopyID, err := s.repo.InsertArchiveCopy(ctx, &storage.ArchiveCopy{
		ParentMessageID: parent.ID,
		VersionID:       version.ID,
		VersionNo:       versionNo,
		ArchiveChatID:   s.cfg.ArchiveChatID,
		SendStatus:      archiveCopyStatusPending,
	})
	if err != nil {
		s.logger.Warnf("record pending archive copy failed parent_message_id=%d version_no=%d: %v", parent.ID, versionNo, err)
	}

	s.logger.Infof("archive send attempt source_chat_id=%d source_message_id=%d content_type=%s method=%s version_no=%d", message.Chat.ID, message.ID, action.contentType, action.method, versionNo)
	result, err := s.sendArchiveAction(ctx, action)
	if err != nil {
		s.logger.Warnf("archive send failed source_chat_id=%d source_message_id=%d content_type=%s method=%s version_no=%d: %v", message.Chat.ID, message.ID, action.contentType, action.method, versionNo, err)
		if pendingCopyID > 0 {
			if updateErr := s.repo.UpdateArchiveCopyOnFailure(ctx, pendingCopyID, summarizeCommandError(err)); updateErr != nil {
				s.logger.Warnf("update archive copy on failure failed copy_id=%d: %v", pendingCopyID, updateErr)
			}
		}
		return version, nil
	}

	archiveMessageID := result.MessageID
	if pendingCopyID > 0 {
		if updateErr := s.repo.UpdateArchiveCopyOnSend(ctx, pendingCopyID, &archiveMessageID, result.MetadataMessageID, time.Now().UTC()); updateErr != nil {
			s.logger.Warnf("update archive copy on send failed copy_id=%d: %v", pendingCopyID, updateErr)
		}
	}

	if updateErr := s.repo.UpdateVersionArchiveMessageID(ctx, version.ID, &archiveMessageID); updateErr != nil {
		s.logger.Warnf("update version archive message id failed version_id=%d: %v", version.ID, updateErr)
	}
	version.ArchiveMessageID = &archiveMessageID

	if !atomicNextVersion {
		if err := s.repo.SetArchiveCopy(ctx, message.BusinessConnectionID, message.Chat.ID, message.ID, s.cfg.ArchiveChatID, archiveMessageID, time.Now().UTC()); err != nil {
			s.logger.Errorf("save archive mapping failed source_chat_id=%d source_message_id=%d archive_message_id=%d: %v", message.Chat.ID, message.ID, archiveMessageID, err)
		}
	}

	s.logger.Infof("archive send success source_chat_id=%d source_message_id=%d content_type=%s method=%s version_no=%d archive_message_id=%d", message.Chat.ID, message.ID, action.contentType, action.method, versionNo, archiveMessageID)
	return version, &archiveMessageID
}

func (s *BusinessMessageService) ensureLatestVersion(ctx context.Context, parent *storage.ArchivedMessage) (*storage.MessageVersion, error) {
	if parent == nil {
		return nil, nil
	}

	latest, err := s.repo.GetLatestVersionByParentID(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	if latest != nil {
		return latest, nil
	}

	bootstrap := &storage.MessageVersion{
		ParentMessageID:  parent.ID,
		VersionNo:        1,
		ContentType:      parent.ContentType,
		ArchiveMessageID: parent.ArchiveMessageID,
		CreatedAt:        parent.CreatedAt,
	}
	insertedID, err := s.repo.InsertVersion(ctx, bootstrap)
	if err != nil {
		return nil, fmt.Errorf("insert bootstrap version: %w", err)
	}
	bootstrap.ID = insertedID
	s.logger.Infof("version created source_chat_id=%d source_message_id=%d version_no=%d bootstrap=true", parent.SourceChatID, parent.SourceMessageID, bootstrap.VersionNo)
	return bootstrap, nil
}

func (s *BusinessMessageService) upsertBusinessTarget(ctx context.Context, record *storage.ArchivedMessage, now time.Time) {
	if record == nil {
		return
	}

	normalizedUsername := normalizeUsernameKey(record.SourceUsername)
	if normalizedUsername == "" {
		return
	}

	if err := s.repo.UpsertBusinessTarget(ctx, &storage.BusinessSendTarget{
		BusinessConnectionID: record.BusinessConnectionID,
		TargetChatID:         record.SourceChatID,
		NormalizedUsername:   normalizedUsername,
		FirstSeenAt:          record.CreatedAt,
		UpdatedAt:            now,
	}); err != nil {
		s.logger.Warnf("upsert business target failed source_chat_id=%d source_username=%s: %v", record.SourceChatID, record.SourceUsername, err)
	}
}

func (s *BusinessMessageService) sendEditNotification(ctx context.Context, parent *storage.ArchivedMessage, oldVersion *storage.MessageVersion, newVersion *storage.MessageVersion, mediaChanged bool) error {
	if parent == nil || oldVersion == nil || newVersion == nil {
		return nil
	}

	lines := []string{
		"<b>Business message edited</b>",
		fmt.Sprintf("Source chat ID: <code>%d</code>", parent.SourceChatID),
		fmt.Sprintf("Source message ID: <code>%d</code>", parent.SourceMessageID),
		fmt.Sprintf("Old version: <code>%d</code>", oldVersion.VersionNo),
		fmt.Sprintf("New version: <code>%d</code>", newVersion.VersionNo),
		fmt.Sprintf("Old type: <code>%s</code>", html.EscapeString(oldVersion.ContentType)),
		fmt.Sprintf("New type: <code>%s</code>", html.EscapeString(newVersion.ContentType)),
	}

	if mediaChanged {
		lines = append(lines, "<b>Media content changed</b>")
	}
	if oldVersion.ArchiveMessageID != nil {
		lines = append(lines, fmt.Sprintf("Old archived version ID: <code>%d</code>", *oldVersion.ArchiveMessageID))
	}
	if newVersion.ArchiveMessageID != nil {
		lines = append(lines, fmt.Sprintf("New archived version ID: <code>%d</code>", *newVersion.ArchiveMessageID))
	}

	if err := s.tg.SendOwnerNotification(ctx, s.cfg.OwnerChatID, strings.Join(lines, "\n")); err != nil {
		return err
	}

	if oldVersion.ArchiveMessageID != nil {
		if _, err := s.tg.CopyMessage(ctx, s.cfg.ArchiveChatID, *oldVersion.ArchiveMessageID, s.cfg.OwnerChatID); err != nil {
			if s.tg.IsMissingMessageError(err) {
				s.logger.Warnf("old archived edit copy missing source_chat_id=%d source_message_id=%d archive_message_id=%d", parent.SourceChatID, parent.SourceMessageID, *oldVersion.ArchiveMessageID)
				return nil
			}
			return fmt.Errorf("copy old archived edit version: %w", err)
		}
	}

	return nil
}

func (s *BusinessMessageService) sendArchiveAction(ctx context.Context, action archiveAction) (archiveSendResult, error) {
	var (
		archiveMessageID int
		err              error
	)

	switch action.method {
	case archiveSendMethodText:
		archiveMessageID, err = s.tg.SendMessage(ctx, s.cfg.ArchiveChatID, action.text)
	case archiveSendMethodPhoto:
		archiveMessageID, err = s.tg.SendPhotoByFileID(ctx, s.cfg.ArchiveChatID, action.fileID, action.caption)
	case archiveSendMethodVoice:
		archiveMessageID, err = s.tg.SendVoiceByFileID(ctx, s.cfg.ArchiveChatID, action.fileID, action.caption)
	case archiveSendMethodAudio:
		archiveMessageID, err = s.tg.SendAudioByFileID(ctx, s.cfg.ArchiveChatID, action.fileID, action.caption)
	case archiveSendMethodDocument:
		archiveMessageID, err = s.tg.SendDocumentByFileID(ctx, s.cfg.ArchiveChatID, action.fileID, action.caption)
	case archiveSendMethodVideo:
		archiveMessageID, err = s.tg.SendVideoByFileID(ctx, s.cfg.ArchiveChatID, action.fileID, action.caption)
	case archiveSendMethodAnimation:
		archiveMessageID, err = s.tg.SendAnimationByFileID(ctx, s.cfg.ArchiveChatID, action.fileID, action.caption)
	case archiveSendMethodSticker:
		archiveMessageID, err = s.tg.SendStickerByFileID(ctx, s.cfg.ArchiveChatID, action.fileID)
	case archiveSendMethodVideoNote:
		archiveMessageID, err = s.tg.SendVideoNoteByFileID(ctx, s.cfg.ArchiveChatID, action.fileID)
	default:
		return archiveSendResult{}, fmt.Errorf("unsupported archive send method: %s", action.method)
	}

	if err != nil {
		return archiveSendResult{}, fmt.Errorf("send archive action: %w", err)
	}

	result := archiveSendResult{MessageID: archiveMessageID}
	if action.metadataText != "" {
		if metadataMessageID, metaErr := s.tg.SendMessage(ctx, s.cfg.ArchiveChatID, action.metadataText); metaErr != nil {
			s.logger.Warnf("archive metadata send failed content_type=%s: %v", action.contentType, metaErr)
		} else {
			result.MetadataMessageID = &metadataMessageID
		}
	}

	return result, nil
}

func (s *BusinessMessageService) HandleDeletedBusinessMessages(ctx context.Context, update *models.BusinessMessagesDeleted) {
	if update == nil {
		return
	}
	if update.BusinessConnectionID == "" {
		s.logger.Warnf("skip deleted_business_messages without business_connection_id source_chat_id=%d", update.Chat.ID)
		return
	}

	for _, sourceMessageID := range update.MessageIDs {
		s.handleDeletedMessageID(ctx, deleteKey{
			businessConnectionID: update.BusinessConnectionID,
			sourceChatID:         update.Chat.ID,
			sourceMessageID:      sourceMessageID,
		})
	}
}

func (s *BusinessMessageService) handleDeletedMessageID(ctx context.Context, key deleteKey) {
	result, err := s.processDelete(ctx, key)
	if err != nil {
		s.logger.Errorf("delete processing failed source_chat_id=%d source_message_id=%d: %v", key.sourceChatID, key.sourceMessageID, err)
		s.enqueuePendingDelete(ctx, key, "processing_error")
		return
	}

	switch result {
	case deleteProcessProcessed, deleteProcessAlreadyProcessed:
		s.removePendingDelete(ctx, key)
	case deleteProcessMissingMessage:
		s.enqueuePendingDelete(ctx, key, "metadata_not_found")
	}
}

func (s *BusinessMessageService) processDelete(ctx context.Context, key deleteKey) (deleteProcessResult, error) {
	record, err := s.repo.GetBySource(ctx, key.businessConnectionID, key.sourceChatID, key.sourceMessageID)
	if err != nil {
		return "", fmt.Errorf("load deleted message metadata: %w", err)
	}
	if record == nil {
		return deleteProcessMissingMessage, nil
	}

	deletedAt := time.Now().UTC()
	updated, err := s.repo.MarkDeletedIfUnset(ctx, key.businessConnectionID, key.sourceChatID, key.sourceMessageID, deletedAt)
	if err != nil {
		return "", fmt.Errorf("mark deleted: %w", err)
	}
	if !updated {
		s.logger.Debugf("duplicate delete event skipped source_chat_id=%d source_message_id=%d", key.sourceChatID, key.sourceMessageID)
		return deleteProcessAlreadyProcessed, nil
	}

	var deletionNotifiedAt *time.Time
	if s.cfg.NotifyOnDelete {
		notificationText := s.buildDeleteNotification(record)
		now := time.Now().UTC()
		deletionNotifiedAt = &now
		if err := s.tg.SendOwnerNotification(ctx, s.cfg.OwnerChatID, notificationText); err != nil {
			s.logger.Errorf("delete notification failed source_chat_id=%d source_message_id=%d: %v", key.sourceChatID, key.sourceMessageID, err)
		} else {
			s.logger.Infof("delete notification sent source_chat_id=%d source_message_id=%d", key.sourceChatID, key.sourceMessageID)
		}
	}

	var resentToOwnerAt *time.Time
	if s.cfg.ResendArchivedCopyOnDelete && record.ArchiveMessageID != nil {
		archiveChatID := s.cfg.ArchiveChatID
		if record.ArchiveChatID != nil && *record.ArchiveChatID != 0 {
			archiveChatID = *record.ArchiveChatID
		}

		_, err := s.tg.CopyMessage(ctx, archiveChatID, *record.ArchiveMessageID, s.cfg.OwnerChatID)
		if err != nil {
			if s.tg.IsMissingMessageError(err) {
				s.logger.Warnf("archived copy missing while resending source_chat_id=%d source_message_id=%d archive_message_id=%d", key.sourceChatID, key.sourceMessageID, *record.ArchiveMessageID)
			} else {
				s.logger.Errorf("resend archived copy failed source_chat_id=%d source_message_id=%d archive_message_id=%d: %v", key.sourceChatID, key.sourceMessageID, *record.ArchiveMessageID, err)
			}
		} else {
			now := time.Now().UTC()
			resentToOwnerAt = &now
			s.logger.Infof("resend archived copy success source_chat_id=%d source_message_id=%d archive_message_id=%d", key.sourceChatID, key.sourceMessageID, *record.ArchiveMessageID)
		}
	}

	// If the deleted message belonged to a media group, also resend siblings.
	if record.MediaGroupID != "" && s.cfg.ResendArchivedCopyOnDelete {
		siblings, sibErr := s.repo.ListByMediaGroup(ctx, record.BusinessConnectionID, record.SourceChatID, record.MediaGroupID)
		if sibErr != nil {
			s.logger.Warnf("media group lookup during delete failed media_group_id=%s: %v", record.MediaGroupID, sibErr)
		} else {
			for _, sibling := range siblings {
				if sibling.SourceMessageID == record.SourceMessageID {
					continue
				}
				if sibling.ArchiveMessageID == nil {
					continue
				}
				archiveChatID := s.cfg.ArchiveChatID
				if sibling.ArchiveChatID != nil && *sibling.ArchiveChatID != 0 {
					archiveChatID = *sibling.ArchiveChatID
				}
				if _, copyErr := s.tg.CopyMessage(ctx, archiveChatID, *sibling.ArchiveMessageID, s.cfg.OwnerChatID); copyErr != nil {
					if !s.tg.IsMissingMessageError(copyErr) {
						s.logger.Warnf("media group sibling resend failed media_group_id=%s sibling_source_message_id=%d: %v", record.MediaGroupID, sibling.SourceMessageID, copyErr)
					}
				}
			}
		}
	}

	if err := s.repo.RecordDeleteProcessing(ctx, key.businessConnectionID, key.sourceChatID, key.sourceMessageID, deletionNotifiedAt, resentToOwnerAt, time.Now().UTC()); err != nil {
		return "", fmt.Errorf("update delete tracking: %w", err)
	}

	s.logger.Infof("delete event processed source_chat_id=%d source_message_id=%d", key.sourceChatID, key.sourceMessageID)
	return deleteProcessProcessed, nil
}

func (s *BusinessMessageService) buildDeleteNotification(record *storage.ArchivedMessage) string {
	if record == nil {
		return "<b>Deleted message detected</b>"
	}

	sourceUsername := formatUsernameOrPlaceholder(record.SourceUsername)
	lines := []string{
		"<b>Deleted message detected</b>",
		fmt.Sprintf("From: <code>%s</code>", html.EscapeString(sourceUsername)),
	}

	if displayName := compactSingleLine(record.SourceDisplayName); displayName != "" {
		lines = append(lines, fmt.Sprintf("Display name: <code>%s</code>", html.EscapeString(displayName)))
	}
	if record.SourceFromID != nil {
		lines = append(lines, fmt.Sprintf("Source from ID: <code>%d</code>", *record.SourceFromID))
	}

	lines = append(lines,
		fmt.Sprintf("Type: <code>%s</code>", html.EscapeString(record.ContentType)),
		fmt.Sprintf("Source chat ID: <code>%d</code>", record.SourceChatID),
		fmt.Sprintf("Source message ID: <code>%d</code>", record.SourceMessageID),
		fmt.Sprintf("Created at (UTC): <code>%s</code>", record.CreatedAt.UTC().Format(time.RFC3339)),
		fmt.Sprintf("Retention expires at (UTC): <code>%s</code>", record.ExpiresAt.UTC().Format(time.RFC3339)),
		fmt.Sprintf("Archived copy existed: <code>%t</code>", record.ArchiveMessageID != nil),
	)

	if record.MediaGroupID != "" {
		lines = append(lines, fmt.Sprintf("Media group: <code>%s</code>", html.EscapeString(record.MediaGroupID)))
	}

	return strings.Join(lines, "\n")
}
