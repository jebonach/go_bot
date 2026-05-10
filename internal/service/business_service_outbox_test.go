package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"removed-messages/internal/storage"
)

func TestHandleBusinessMessage_OutboxPendingThenSent(t *testing.T) {
	logger := newTestLogger(t)

	pendingCopyInserted := false
	var sentCopyID int64
	var sentCopyArchiveID *int

	repo := &mockRepo{
		insertIfNotExistsFn: func(context.Context, *storage.ArchivedMessage) (bool, error) { return true, nil },
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   1,
				BusinessConnectionID: "bc-outbox",
				SourceChatID:         100,
				SourceMessageID:      10,
				CreatedAt:            time.Now().UTC(),
			}, nil
		},
		insertVersionFn: func(context.Context, *storage.MessageVersion) (int64, error) { return 1, nil },
		insertArchiveCopyFn: func(_ context.Context, copy *storage.ArchiveCopy) (int64, error) {
			if copy.SendStatus != archiveCopyStatusPending {
				t.Fatalf("expected pending status before send, got %q", copy.SendStatus)
			}
			if copy.ArchiveMessageID != nil {
				t.Fatalf("expected NULL archive message id in pending row")
			}
			pendingCopyInserted = true
			return 42, nil
		},
		updateArchiveCopyOnSendFn: func(_ context.Context, id int64, archiveMessageID *int, _ *int, _ time.Time) error {
			sentCopyID = id
			sentCopyArchiveID = archiveMessageID
			return nil
		},
		setArchiveCopyFn: func(context.Context, string, int64, int, int64, int, time.Time) error { return nil },
	}
	tg := &mockTelegram{
		sendMessageFn: func(context.Context, int64, string) (int, error) { return 555, nil },
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessMessage(context.Background(), &models.Message{
		ID:                   10,
		BusinessConnectionID: "bc-outbox",
		Chat:                 models.Chat{ID: 100},
		Text:                 "hello",
		Date:                 int(time.Now().Unix()),
	})

	if !pendingCopyInserted {
		t.Fatalf("expected pending archive copy to be inserted before send")
	}
	if sentCopyID != 42 {
		t.Fatalf("expected pending row 42 to be marked sent, got %d", sentCopyID)
	}
	if sentCopyArchiveID == nil || *sentCopyArchiveID != 555 {
		t.Fatalf("expected archive message id 555 to be persisted, got %v", sentCopyArchiveID)
	}
}

func TestHandleBusinessMessage_OutboxMarksFailedOnSendError(t *testing.T) {
	logger := newTestLogger(t)

	failureCopyID := int64(0)
	var failureText string

	repo := &mockRepo{
		insertIfNotExistsFn: func(context.Context, *storage.ArchivedMessage) (bool, error) { return true, nil },
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{ID: 2, CreatedAt: time.Now().UTC()}, nil
		},
		insertVersionFn:     func(context.Context, *storage.MessageVersion) (int64, error) { return 5, nil },
		insertArchiveCopyFn: func(context.Context, *storage.ArchiveCopy) (int64, error) { return 99, nil },
		updateArchiveCopyOnFailureFn: func(_ context.Context, id int64, errorText string) error {
			failureCopyID = id
			failureText = errorText
			return nil
		},
	}
	tg := &mockTelegram{
		sendMessageFn: func(context.Context, int64, string) (int, error) {
			return 0, errors.New("simulated archive failure")
		},
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessMessage(context.Background(), &models.Message{
		ID:                   11,
		BusinessConnectionID: "bc-fail",
		Chat:                 models.Chat{ID: 200},
		Text:                 "fail",
		Date:                 int(time.Now().Unix()),
	})

	if failureCopyID != 99 {
		t.Fatalf("expected copy 99 to be marked failed, got %d", failureCopyID)
	}
	if failureText == "" {
		t.Fatalf("expected failure error text to be recorded")
	}
}

func TestHandleEditedBusinessMessage_DuplicateEditDateIgnored(t *testing.T) {
	logger := newTestLogger(t)
	insertNextCalls := 0
	editDate := int64(1700000000)

	repo := &mockRepo{
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   3,
				BusinessConnectionID: "bc-edit-dup",
				SourceChatID:         300,
				SourceMessageID:      33,
				ContentType:          ContentTypeText,
				CreatedAt:            time.Now().UTC().Add(-time.Minute),
			}, nil
		},
		getLatestVersionByParentIDFn: func(context.Context, int64) (*storage.MessageVersion, error) {
			return &storage.MessageVersion{
				ID:          1,
				VersionNo:   2,
				ContentType: ContentTypeText,
				EditDate:    &editDate,
				CreatedAt:   time.Now().UTC().Add(-time.Minute),
			}, nil
		},
		insertNextVersionFn: func(context.Context, *storage.MessageVersion) (int64, int, error) {
			insertNextCalls++
			return 4, 3, nil
		},
	}
	tg := &mockTelegram{
		sendMessageFn:           func(context.Context, int64, string) (int, error) { return 88, nil },
		sendOwnerNotificationFn: func(context.Context, int64, string) error { return nil },
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleEditedBusinessMessage(context.Background(), &models.Message{
		ID:                   33,
		BusinessConnectionID: "bc-edit-dup",
		Chat:                 models.Chat{ID: 300},
		Text:                 "edited again",
		EditDate:             int(editDate),
	})

	if insertNextCalls != 0 {
		t.Fatalf("expected duplicate edit_date to skip new version insert, got %d", insertNextCalls)
	}
}

func TestHandleOwnerCommand_DeniesGroupChat(t *testing.T) {
	logger := newTestLogger(t)
	var summary string
	repo := &mockRepo{}
	tg := &mockTelegram{
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			summary = text
			return 0, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: -1009999}, // group chat id
		From: &models.User{ID: 12345},   // owner
		Text: "/send [@alice] [hi]",
	})

	if !handled {
		t.Fatalf("expected /send to be handled (with rejection)")
	}
	if summary == "" || summary == sendCommandUsage {
		t.Fatalf("expected restriction message, got %q", summary)
	}
}

func TestProcessDelete_ResendsMediaGroupSiblings(t *testing.T) {
	logger := newTestLogger(t)

	primaryArchiveID := 700
	siblingOneArchiveID := 701
	siblingTwoArchiveID := 702
	copiedArchiveIDs := make([]int, 0, 3)

	repo := &mockRepo{
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   12,
				BusinessConnectionID: "bc-album",
				SourceChatID:         400,
				SourceMessageID:      40,
				ContentType:          ContentTypePhoto,
				MediaGroupID:         "album-1",
				ArchiveMessageID:     &primaryArchiveID,
				CreatedAt:            time.Now().UTC().Add(-time.Minute),
				ExpiresAt:            time.Now().UTC().Add(time.Hour),
			}, nil
		},
		markDeletedIfUnsetFn: func(context.Context, string, int64, int, time.Time) (bool, error) {
			return true, nil
		},
		listByMediaGroupFn: func(context.Context, string, int64, string) ([]storage.ArchivedMessage, error) {
			return []storage.ArchivedMessage{
				{SourceMessageID: 40, ArchiveMessageID: &primaryArchiveID, MediaGroupID: "album-1"},
				{SourceMessageID: 41, ArchiveMessageID: &siblingOneArchiveID, MediaGroupID: "album-1"},
				{SourceMessageID: 42, ArchiveMessageID: &siblingTwoArchiveID, MediaGroupID: "album-1"},
			}, nil
		},
	}
	tg := &mockTelegram{
		copyMessageFn: func(_ context.Context, _ int64, messageID int, _ int64) (int, error) {
			copiedArchiveIDs = append(copiedArchiveIDs, messageID)
			return messageID + 1000, nil
		},
		sendOwnerNotificationFn: func(context.Context, int64, string) error { return nil },
	}

	svc := newTestService(t, logger, repo, tg)
	if _, err := svc.processDelete(context.Background(), deleteKey{
		businessConnectionID: "bc-album",
		sourceChatID:         400,
		sourceMessageID:      40,
	}); err != nil {
		t.Fatalf("processDelete returned error: %v", err)
	}

	// primary copy + 2 siblings = 3 total CopyMessage calls
	if len(copiedArchiveIDs) != 3 {
		t.Fatalf("expected 3 copies (primary + 2 siblings), got %d: %v", len(copiedArchiveIDs), copiedArchiveIDs)
	}
}
