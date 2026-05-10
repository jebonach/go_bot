package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"removed-messages/internal/config"
	"removed-messages/internal/logging"
	"removed-messages/internal/storage"
)

func TestBuildArchiveActionWithVersion_UsesLargestPhotoFileID(t *testing.T) {
	message := &models.Message{
		ID:                   10,
		BusinessConnectionID: "bc1",
		Chat:                 models.Chat{ID: 101},
		Photo: []models.PhotoSize{
			{FileID: "small"},
			{FileID: "large"},
		},
	}

	classification := ClassifyMessage(message)
	action, ok := buildArchiveActionWithVersion(message, classification, 2)
	if !ok {
		t.Fatalf("expected action for photo")
	}
	if action.method != archiveSendMethodPhoto {
		t.Fatalf("unexpected method: %s", action.method)
	}
	if action.fileID != "large" {
		t.Fatalf("expected largest photo file_id, got %q", action.fileID)
	}
	if action.caption == "" {
		t.Fatalf("expected non-empty archive header caption")
	}
}

func TestBuildArchiveAction_DoesNotExposeBusinessConnectionID(t *testing.T) {
	message := &models.Message{
		ID:                   11,
		BusinessConnectionID: "sensitive-business-connection",
		Chat:                 models.Chat{ID: 111},
		Text:                 "hello",
	}

	classification := ClassifyMessage(message)
	action, ok := buildArchiveActionWithVersion(message, classification, 1)
	if !ok {
		t.Fatalf("expected action for text")
	}
	if strings.Contains(action.text, "business_connection_id") || strings.Contains(action.text, message.BusinessConnectionID) {
		t.Fatalf("archive text must not expose business connection id: %q", action.text)
	}
}

func TestBuildArchiveAction_ArchivesUnknownAsMetadata(t *testing.T) {
	message := &models.Message{
		ID:                   12,
		BusinessConnectionID: "sensitive-business-connection",
		Chat:                 models.Chat{ID: 112},
		Date:                 int(time.Now().Unix()),
	}

	classification := ClassifyMessage(message)
	if !classification.Archivable || classification.ContentType != ContentTypeUnknown {
		t.Fatalf("expected archivable unknown classification, got %+v", classification)
	}

	action, ok := buildArchiveActionWithVersion(message, classification, 1)
	if !ok {
		t.Fatalf("expected metadata archive action for unknown")
	}
	if action.method != archiveSendMethodText {
		t.Fatalf("expected text archive method, got %s", action.method)
	}
	if strings.Contains(action.text, "business_connection_id") || strings.Contains(action.text, message.BusinessConnectionID) {
		t.Fatalf("unknown metadata must not expose business connection id: %q", action.text)
	}
}

func TestHandleBusinessMessage_CreatesVersionOne(t *testing.T) {
	logger := newTestLogger(t)
	repo := &mockRepo{
		insertIfNotExistsFn: func(context.Context, *storage.ArchivedMessage) (bool, error) { return true, nil },
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   1,
				BusinessConnectionID: "bc2",
				SourceChatID:         202,
				SourceMessageID:      20,
				CreatedAt:            time.Now().UTC(),
			}, nil
		},
		insertVersionFn: func(context.Context, *storage.MessageVersion) (int64, error) { return 1, nil },
		setArchiveCopyFn: func(context.Context, string, int64, int, int64, int, time.Time) error {
			return nil
		},
	}
	tg := &mockTelegram{
		sendVoiceByFileIDFn: func(context.Context, int64, string, string) (int, error) { return 55, nil },
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessMessage(context.Background(), &models.Message{
		ID:                   20,
		BusinessConnectionID: "bc2",
		Chat:                 models.Chat{ID: 202},
		Voice:                &models.Voice{FileID: "voice-id"},
		Date:                 int(time.Now().Unix()),
	})

	if repo.insertVersionCalls != 1 {
		t.Fatalf("expected one version insert, got %d", repo.insertVersionCalls)
	}
}

func TestHandleBusinessMessage_DoesNotStoreMessageContentInSQLModels(t *testing.T) {
	logger := newTestLogger(t)
	var insertedMessage *storage.ArchivedMessage
	var insertedVersion *storage.MessageVersion

	repo := &mockRepo{
		insertIfNotExistsFn: func(_ context.Context, message *storage.ArchivedMessage) (bool, error) {
			copyMessage := *message
			insertedMessage = &copyMessage
			return true, nil
		},
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   101,
				BusinessConnectionID: "bc-content",
				SourceChatID:         404,
				SourceMessageID:      44,
				CreatedAt:            time.Now().UTC(),
			}, nil
		},
		insertVersionFn: func(_ context.Context, version *storage.MessageVersion) (int64, error) {
			copyVersion := *version
			insertedVersion = &copyVersion
			return 1, nil
		},
		setArchiveCopyFn: func(context.Context, string, int64, int, int64, int, time.Time) error {
			return nil
		},
	}
	tg := &mockTelegram{
		sendMessageFn: func(context.Context, int64, string) (int, error) { return 700, nil },
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessMessage(context.Background(), &models.Message{
		ID:                   44,
		BusinessConnectionID: "bc-content",
		Chat:                 models.Chat{ID: 404},
		From:                 &models.User{ID: 1, Username: "alice"},
		Text:                 "secret message body",
		Date:                 int(time.Now().Unix()),
	})

	if insertedMessage == nil || insertedVersion == nil {
		t.Fatalf("expected message and version inserts")
	}
	if insertedMessage.ContentType != ContentTypeText || insertedVersion.ContentType != ContentTypeText {
		t.Fatalf("expected only technical content type metadata, got message=%+v version=%+v", insertedMessage, insertedVersion)
	}
}

func TestHandleEditedBusinessMessage_CreatesNextVersion(t *testing.T) {
	logger := newTestLogger(t)
	repo := &mockRepo{
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   7,
				BusinessConnectionID: "bc3",
				SourceChatID:         303,
				SourceMessageID:      30,
				ContentType:          ContentTypeText,
				CreatedAt:            time.Now().UTC().Add(-time.Minute),
			}, nil
		},
		getLatestVersionByParentIDFn: func(context.Context, int64) (*storage.MessageVersion, error) {
			return &storage.MessageVersion{
				ID:          1,
				VersionNo:   1,
				ContentType: ContentTypeText,
				CreatedAt:   time.Now().UTC().Add(-time.Minute),
			}, nil
		},
		insertNextVersionFn: func(context.Context, *storage.MessageVersion) (int64, int, error) { return 2, 2, nil },
		updateCurrentFromVersionFn: func(context.Context, string, int64, int, string, string, *int64, *int, time.Time) error {
			return nil
		},
	}
	tg := &mockTelegram{
		sendMessageFn:           func(context.Context, int64, string) (int, error) { return 77, nil },
		sendOwnerNotificationFn: func(context.Context, int64, string) error { return nil },
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleEditedBusinessMessage(context.Background(), &models.Message{
		ID:                   30,
		BusinessConnectionID: "bc3",
		Chat:                 models.Chat{ID: 303},
		Text:                 "new",
	})

	if repo.insertNextVersionCalls != 1 {
		t.Fatalf("expected one new version insert, got %d", repo.insertNextVersionCalls)
	}
}

func TestHandleEditedBusinessMessage_CopiesOldArchivedVersionToOwner(t *testing.T) {
	logger := newTestLogger(t)
	oldArchiveID := 501
	copiedArchiveID := 0

	repo := &mockRepo{
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   8,
				BusinessConnectionID: "bc-copy-old",
				SourceChatID:         808,
				SourceMessageID:      80,
				ContentType:          ContentTypeText,
				CreatedAt:            time.Now().UTC().Add(-time.Minute),
			}, nil
		},
		getLatestVersionByParentIDFn: func(context.Context, int64) (*storage.MessageVersion, error) {
			return &storage.MessageVersion{
				ID:               1,
				VersionNo:        1,
				ContentType:      ContentTypeText,
				ArchiveMessageID: &oldArchiveID,
				CreatedAt:        time.Now().UTC().Add(-time.Minute),
			}, nil
		},
		insertNextVersionFn: func(context.Context, *storage.MessageVersion) (int64, int, error) { return 2, 2, nil },
		updateCurrentFromVersionFn: func(context.Context, string, int64, int, string, string, *int64, *int, time.Time) error {
			return nil
		},
	}
	tg := &mockTelegram{
		sendMessageFn:           func(context.Context, int64, string) (int, error) { return 78, nil },
		sendOwnerNotificationFn: func(context.Context, int64, string) error { return nil },
		copyMessageFn: func(_ context.Context, _ int64, messageID int, _ int64) (int, error) {
			copiedArchiveID = messageID
			return 79, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleEditedBusinessMessage(context.Background(), &models.Message{
		ID:                   80,
		BusinessConnectionID: "bc-copy-old",
		Chat:                 models.Chat{ID: 808},
		Text:                 "new content",
	})

	if copiedArchiveID != oldArchiveID {
		t.Fatalf("expected old archive version %d to be copied, got %d", oldArchiveID, copiedArchiveID)
	}
}

func TestCleanupRecord_DeletesAllArchiveCopies(t *testing.T) {
	logger := newTestLogger(t)
	deletedMessages := make([]int, 0)
	markedDeleted := make([]int64, 0)

	repo := &mockRepo{
		listArchiveCopiesByMessageIDFn: func(context.Context, int64) ([]storage.ArchiveCopy, error) {
			firstArchiveID := 101
			firstMetaID := 102
			secondArchiveID := 201
			return []storage.ArchiveCopy{
				{ID: 1, ParentMessageID: 99, VersionNo: 1, ArchiveChatID: -100777, ArchiveMessageID: &firstArchiveID, MetadataMessageID: &firstMetaID, SendStatus: archiveCopyStatusSent},
				{ID: 2, ParentMessageID: 99, VersionNo: 2, ArchiveChatID: -100777, ArchiveMessageID: &secondArchiveID, SendStatus: archiveCopyStatusSent},
			}, nil
		},
		markArchiveCopyDeletedFn: func(_ context.Context, id int64, _ time.Time) error {
			markedDeleted = append(markedDeleted, id)
			return nil
		},
		deleteByIDFn: func(context.Context, int64) error { return nil },
	}
	tg := &mockTelegram{
		deleteMessageFn: func(_ context.Context, _ int64, messageID int) error {
			deletedMessages = append(deletedMessages, messageID)
			return nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	if !svc.cleanupRecord(context.Background(), &storage.ArchivedMessage{ID: 99}) {
		t.Fatalf("expected cleanup to succeed")
	}
	if len(deletedMessages) != 3 {
		t.Fatalf("expected 3 archive telegram deletes, got %d: %v", len(deletedMessages), deletedMessages)
	}
	if len(markedDeleted) != 2 {
		t.Fatalf("expected 2 archive copy marks, got %d: %v", len(markedDeleted), markedDeleted)
	}
}

func newTestService(t *testing.T, logger *logging.Logger, repo storage.Repository, tg TelegramClient) *BusinessMessageService {
	t.Helper()

	cfg := &config.Config{
		ArchiveChatID:              -100777,
		OwnerChatID:                12345,
		NotifyOnDelete:             true,
		ResendArchivedCopyOnDelete: true,
		DeleteExpiredFromArchive:   true,
		CleanupInterval:            time.Second,
	}

	svc := NewBusinessMessageService(cfg, repo, tg, logger)
	return svc
}

func newTestLogger(t *testing.T) *logging.Logger {
	t.Helper()
	logger, err := logging.New("error")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	return logger
}

type mockRepo struct {
	migrateFn                          func(context.Context) error
	insertIfNotExistsFn                func(context.Context, *storage.ArchivedMessage) (bool, error)
	setArchiveCopyFn                   func(context.Context, string, int64, int, int64, int, time.Time) error
	getBySourceFn                      func(context.Context, string, int64, int) (*storage.ArchivedMessage, error)
	upsertBusinessTargetFn             func(context.Context, *storage.BusinessSendTarget) error
	findLatestChatTargetByUsernameFn   func(context.Context, string) (*storage.BusinessSendTarget, error)
	updateCurrentFromVersionFn         func(context.Context, string, int64, int, string, string, *int64, *int, time.Time) error
	insertVersionFn                    func(context.Context, *storage.MessageVersion) (int64, error)
	insertNextVersionFn                func(context.Context, *storage.MessageVersion) (int64, int, error)
	updateVersionArchiveMessageIDFn    func(context.Context, int64, *int) error
	getLatestVersionByParentIDFn       func(context.Context, int64) (*storage.MessageVersion, error)
	getLatestVersionBySourceFn         func(context.Context, string, int64, int) (*storage.MessageVersion, error)
	getVersionByParentAndNumberFn      func(context.Context, int64, int) (*storage.MessageVersion, error)
	insertArchiveCopyFn                func(context.Context, *storage.ArchiveCopy) (int64, error)
	updateArchiveCopyOnSendFn          func(context.Context, int64, *int, *int, time.Time) error
	updateArchiveCopyOnFailureFn       func(context.Context, int64, string) error
	listArchiveCopiesByMessageIDFn     func(context.Context, int64) ([]storage.ArchiveCopy, error)
	listPendingArchiveCopiesOlderThanFn func(context.Context, time.Time, int) ([]storage.ArchiveCopy, error)
	listByMediaGroupFn                 func(context.Context, string, int64, string) ([]storage.ArchivedMessage, error)
	markArchiveCopyDeletedFn           func(context.Context, int64, time.Time) error
	markDeletedIfUnsetFn               func(context.Context, string, int64, int, time.Time) (bool, error)
	recordDeleteProcessingFn           func(context.Context, string, int64, int, *time.Time, *time.Time, time.Time) error
	upsertPendingDeleteFn              func(context.Context, *storage.PendingDelete) error
	getPendingDeleteFn                 func(context.Context, string, int64, int) (*storage.PendingDelete, error)
	listDuePendingDeletesFn            func(context.Context, time.Time, int) ([]storage.PendingDelete, error)
	deletePendingDeleteFn              func(context.Context, string, int64, int) error
	reschedulePendingDeleteFn          func(context.Context, string, int64, int, time.Time, int, string, time.Time) error
	listExpiredFn                      func(context.Context, time.Time, int) ([]storage.ArchivedMessage, error)
	deleteByIDFn                       func(context.Context, int64) error
	upsertBusinessConnectionFn         func(context.Context, *storage.BusinessConnection) error
	getBusinessConnectionFn            func(context.Context, string) (*storage.BusinessConnection, error)
	listBusinessConnectionsFn          func(context.Context, bool) ([]storage.BusinessConnection, error)
	closeFn                            func() error

	insertVersionCalls     int
	insertNextVersionCalls int
}

func (m *mockRepo) Migrate(ctx context.Context) error {
	if m.migrateFn != nil {
		return m.migrateFn(ctx)
	}
	return nil
}

func (m *mockRepo) InsertIfNotExists(ctx context.Context, message *storage.ArchivedMessage) (bool, error) {
	if m.insertIfNotExistsFn != nil {
		return m.insertIfNotExistsFn(ctx, message)
	}
	return false, nil
}

func (m *mockRepo) SetArchiveCopy(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, archiveChatID int64, archiveMessageID int, updatedAt time.Time) error {
	if m.setArchiveCopyFn != nil {
		return m.setArchiveCopyFn(ctx, businessConnectionID, sourceChatID, sourceMessageID, archiveChatID, archiveMessageID, updatedAt)
	}
	return nil
}

func (m *mockRepo) GetBySource(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*storage.ArchivedMessage, error) {
	if m.getBySourceFn != nil {
		return m.getBySourceFn(ctx, businessConnectionID, sourceChatID, sourceMessageID)
	}
	return nil, nil
}

func (m *mockRepo) UpsertBusinessTarget(ctx context.Context, target *storage.BusinessSendTarget) error {
	if m.upsertBusinessTargetFn != nil {
		return m.upsertBusinessTargetFn(ctx, target)
	}
	return nil
}

func (m *mockRepo) FindLatestChatTargetByUsername(ctx context.Context, normalizedUsername string) (*storage.BusinessSendTarget, error) {
	if m.findLatestChatTargetByUsernameFn != nil {
		return m.findLatestChatTargetByUsernameFn(ctx, normalizedUsername)
	}
	return nil, nil
}

func (m *mockRepo) UpdateCurrentFromVersion(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, messageKind string, contentType string, archiveChatID *int64, archiveMessageID *int, updatedAt time.Time) error {
	if m.updateCurrentFromVersionFn != nil {
		return m.updateCurrentFromVersionFn(ctx, businessConnectionID, sourceChatID, sourceMessageID, messageKind, contentType, archiveChatID, archiveMessageID, updatedAt)
	}
	return nil
}

func (m *mockRepo) InsertVersion(ctx context.Context, version *storage.MessageVersion) (int64, error) {
	m.insertVersionCalls++
	if m.insertVersionFn != nil {
		return m.insertVersionFn(ctx, version)
	}
	return 0, nil
}

func (m *mockRepo) InsertNextVersion(ctx context.Context, version *storage.MessageVersion) (int64, int, error) {
	m.insertNextVersionCalls++
	if m.insertNextVersionFn != nil {
		return m.insertNextVersionFn(ctx, version)
	}
	return 0, 0, nil
}

func (m *mockRepo) UpdateVersionArchiveMessageID(ctx context.Context, versionID int64, archiveMessageID *int) error {
	if m.updateVersionArchiveMessageIDFn != nil {
		return m.updateVersionArchiveMessageIDFn(ctx, versionID, archiveMessageID)
	}
	return nil
}

func (m *mockRepo) UpdateArchiveCopyOnSend(ctx context.Context, id int64, archiveMessageID *int, metadataMessageID *int, sentAt time.Time) error {
	if m.updateArchiveCopyOnSendFn != nil {
		return m.updateArchiveCopyOnSendFn(ctx, id, archiveMessageID, metadataMessageID, sentAt)
	}
	return nil
}

func (m *mockRepo) UpdateArchiveCopyOnFailure(ctx context.Context, id int64, errorText string) error {
	if m.updateArchiveCopyOnFailureFn != nil {
		return m.updateArchiveCopyOnFailureFn(ctx, id, errorText)
	}
	return nil
}

func (m *mockRepo) ListPendingArchiveCopiesOlderThan(ctx context.Context, threshold time.Time, limit int) ([]storage.ArchiveCopy, error) {
	if m.listPendingArchiveCopiesOlderThanFn != nil {
		return m.listPendingArchiveCopiesOlderThanFn(ctx, threshold, limit)
	}
	return nil, nil
}

func (m *mockRepo) ListByMediaGroup(ctx context.Context, businessConnectionID string, sourceChatID int64, mediaGroupID string) ([]storage.ArchivedMessage, error) {
	if m.listByMediaGroupFn != nil {
		return m.listByMediaGroupFn(ctx, businessConnectionID, sourceChatID, mediaGroupID)
	}
	return nil, nil
}

func (m *mockRepo) GetLatestVersionByParentID(ctx context.Context, parentMessageID int64) (*storage.MessageVersion, error) {
	if m.getLatestVersionByParentIDFn != nil {
		return m.getLatestVersionByParentIDFn(ctx, parentMessageID)
	}
	return nil, nil
}

func (m *mockRepo) GetLatestVersionBySource(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*storage.MessageVersion, error) {
	if m.getLatestVersionBySourceFn != nil {
		return m.getLatestVersionBySourceFn(ctx, businessConnectionID, sourceChatID, sourceMessageID)
	}
	return nil, nil
}

func (m *mockRepo) GetVersionByParentAndNumber(ctx context.Context, parentMessageID int64, versionNo int) (*storage.MessageVersion, error) {
	if m.getVersionByParentAndNumberFn != nil {
		return m.getVersionByParentAndNumberFn(ctx, parentMessageID, versionNo)
	}
	return nil, nil
}

func (m *mockRepo) InsertArchiveCopy(ctx context.Context, copy *storage.ArchiveCopy) (int64, error) {
	if m.insertArchiveCopyFn != nil {
		return m.insertArchiveCopyFn(ctx, copy)
	}
	return 0, nil
}

func (m *mockRepo) ListArchiveCopiesByMessageID(ctx context.Context, parentMessageID int64) ([]storage.ArchiveCopy, error) {
	if m.listArchiveCopiesByMessageIDFn != nil {
		return m.listArchiveCopiesByMessageIDFn(ctx, parentMessageID)
	}
	return nil, nil
}

func (m *mockRepo) MarkArchiveCopyDeleted(ctx context.Context, id int64, deletedAt time.Time) error {
	if m.markArchiveCopyDeletedFn != nil {
		return m.markArchiveCopyDeletedFn(ctx, id, deletedAt)
	}
	return nil
}

func (m *mockRepo) MarkDeletedIfUnset(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, deletedAt time.Time) (bool, error) {
	if m.markDeletedIfUnsetFn != nil {
		return m.markDeletedIfUnsetFn(ctx, businessConnectionID, sourceChatID, sourceMessageID, deletedAt)
	}
	return false, nil
}

func (m *mockRepo) RecordDeleteProcessing(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, deletionNotifiedAt *time.Time, resentToOwnerAt *time.Time, updatedAt time.Time) error {
	if m.recordDeleteProcessingFn != nil {
		return m.recordDeleteProcessingFn(ctx, businessConnectionID, sourceChatID, sourceMessageID, deletionNotifiedAt, resentToOwnerAt, updatedAt)
	}
	return nil
}

func (m *mockRepo) UpsertPendingDelete(ctx context.Context, pending *storage.PendingDelete) error {
	if m.upsertPendingDeleteFn != nil {
		return m.upsertPendingDeleteFn(ctx, pending)
	}
	return nil
}

func (m *mockRepo) GetPendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*storage.PendingDelete, error) {
	if m.getPendingDeleteFn != nil {
		return m.getPendingDeleteFn(ctx, businessConnectionID, sourceChatID, sourceMessageID)
	}
	return nil, nil
}

func (m *mockRepo) ListDuePendingDeletes(ctx context.Context, now time.Time, limit int) ([]storage.PendingDelete, error) {
	if m.listDuePendingDeletesFn != nil {
		return m.listDuePendingDeletesFn(ctx, now, limit)
	}
	return nil, nil
}

func (m *mockRepo) DeletePendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) error {
	if m.deletePendingDeleteFn != nil {
		return m.deletePendingDeleteFn(ctx, businessConnectionID, sourceChatID, sourceMessageID)
	}
	return nil
}

func (m *mockRepo) ReschedulePendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, nextAttemptAt time.Time, attemptCount int, lastError string, updatedAt time.Time) error {
	if m.reschedulePendingDeleteFn != nil {
		return m.reschedulePendingDeleteFn(ctx, businessConnectionID, sourceChatID, sourceMessageID, nextAttemptAt, attemptCount, lastError, updatedAt)
	}
	return nil
}

func (m *mockRepo) ListExpired(ctx context.Context, now time.Time, limit int) ([]storage.ArchivedMessage, error) {
	if m.listExpiredFn != nil {
		return m.listExpiredFn(ctx, now, limit)
	}
	return nil, nil
}

func (m *mockRepo) DeleteByID(ctx context.Context, id int64) error {
	if m.deleteByIDFn != nil {
		return m.deleteByIDFn(ctx, id)
	}
	return nil
}

func (m *mockRepo) UpsertBusinessConnection(ctx context.Context, connection *storage.BusinessConnection) error {
	if m.upsertBusinessConnectionFn != nil {
		return m.upsertBusinessConnectionFn(ctx, connection)
	}
	return nil
}

func (m *mockRepo) GetBusinessConnection(ctx context.Context, id string) (*storage.BusinessConnection, error) {
	if m.getBusinessConnectionFn != nil {
		return m.getBusinessConnectionFn(ctx, id)
	}
	return nil, nil
}

func (m *mockRepo) ListBusinessConnections(ctx context.Context, onlyEnabled bool) ([]storage.BusinessConnection, error) {
	if m.listBusinessConnectionsFn != nil {
		return m.listBusinessConnectionsFn(ctx, onlyEnabled)
	}
	return nil, nil
}

func (m *mockRepo) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

type mockTelegram struct {
	copyMessageFn           func(context.Context, int64, int, int64) (int, error)
	deleteMessageFn         func(context.Context, int64, int) error
	sendMessageFn           func(context.Context, int64, string) (int, error)
	sendBusinessMessageFn   func(context.Context, string, int64, string) (int, error)
	sendPhotoByFileIDFn     func(context.Context, int64, string, string) (int, error)
	sendVoiceByFileIDFn     func(context.Context, int64, string, string) (int, error)
	sendAudioByFileIDFn     func(context.Context, int64, string, string) (int, error)
	sendDocumentByFileIDFn  func(context.Context, int64, string, string) (int, error)
	sendVideoByFileIDFn     func(context.Context, int64, string, string) (int, error)
	sendAnimationByFileIDFn func(context.Context, int64, string, string) (int, error)
	sendStickerByFileIDFn   func(context.Context, int64, string) (int, error)
	sendVideoNoteByFileIDFn func(context.Context, int64, string) (int, error)
	sendOwnerNotificationFn func(context.Context, int64, string) error
	isMissingMessageErrorFn func(error) bool
}

func (m *mockTelegram) CopyMessage(ctx context.Context, fromChatID int64, messageID int, toChatID int64) (int, error) {
	if m.copyMessageFn != nil {
		return m.copyMessageFn(ctx, fromChatID, messageID, toChatID)
	}
	return 0, nil
}

func (m *mockTelegram) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	if m.deleteMessageFn != nil {
		return m.deleteMessageFn(ctx, chatID, messageID)
	}
	return nil
}

func (m *mockTelegram) SendMessage(ctx context.Context, chatID int64, text string) (int, error) {
	if m.sendMessageFn != nil {
		return m.sendMessageFn(ctx, chatID, text)
	}
	return 0, nil
}

func (m *mockTelegram) SendBusinessMessage(ctx context.Context, businessConnectionID string, chatID int64, text string) (int, error) {
	if m.sendBusinessMessageFn != nil {
		return m.sendBusinessMessageFn(ctx, businessConnectionID, chatID, text)
	}
	return 0, nil
}

func (m *mockTelegram) SendPhotoByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	if m.sendPhotoByFileIDFn != nil {
		return m.sendPhotoByFileIDFn(ctx, chatID, fileID, caption)
	}
	return 0, nil
}

func (m *mockTelegram) SendVoiceByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	if m.sendVoiceByFileIDFn != nil {
		return m.sendVoiceByFileIDFn(ctx, chatID, fileID, caption)
	}
	return 0, nil
}

func (m *mockTelegram) SendAudioByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	if m.sendAudioByFileIDFn != nil {
		return m.sendAudioByFileIDFn(ctx, chatID, fileID, caption)
	}
	return 0, nil
}

func (m *mockTelegram) SendDocumentByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	if m.sendDocumentByFileIDFn != nil {
		return m.sendDocumentByFileIDFn(ctx, chatID, fileID, caption)
	}
	return 0, nil
}

func (m *mockTelegram) SendVideoByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	if m.sendVideoByFileIDFn != nil {
		return m.sendVideoByFileIDFn(ctx, chatID, fileID, caption)
	}
	return 0, nil
}

func (m *mockTelegram) SendAnimationByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	if m.sendAnimationByFileIDFn != nil {
		return m.sendAnimationByFileIDFn(ctx, chatID, fileID, caption)
	}
	return 0, nil
}

func (m *mockTelegram) SendStickerByFileID(ctx context.Context, chatID int64, fileID string) (int, error) {
	if m.sendStickerByFileIDFn != nil {
		return m.sendStickerByFileIDFn(ctx, chatID, fileID)
	}
	return 0, nil
}

func (m *mockTelegram) SendVideoNoteByFileID(ctx context.Context, chatID int64, fileID string) (int, error) {
	if m.sendVideoNoteByFileIDFn != nil {
		return m.sendVideoNoteByFileIDFn(ctx, chatID, fileID)
	}
	return 0, nil
}

func (m *mockTelegram) SendOwnerNotification(ctx context.Context, ownerChatID int64, text string) error {
	if m.sendOwnerNotificationFn != nil {
		return m.sendOwnerNotificationFn(ctx, ownerChatID, text)
	}
	return nil
}

func (m *mockTelegram) IsMissingMessageError(err error) bool {
	if m.isMissingMessageErrorFn != nil {
		return m.isMissingMessageErrorFn(err)
	}
	return false
}
