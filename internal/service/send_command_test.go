package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"removed-messages/internal/storage"
)

func TestHandleBusinessMessage_StoresSourceUsernameForText(t *testing.T) {
	logger := newTestLogger(t)
	var inserted *storage.ArchivedMessage

	repo := &mockRepo{
		insertIfNotExistsFn: func(_ context.Context, message *storage.ArchivedMessage) (bool, error) {
			copyMessage := *message
			inserted = &copyMessage
			return true, nil
		},
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   10,
				BusinessConnectionID: "bc-text",
				SourceChatID:         1001,
				SourceMessageID:      1,
				CreatedAt:            time.Now().UTC(),
			}, nil
		},
		insertVersionFn: func(context.Context, *storage.MessageVersion) (int64, error) { return 1, nil },
		setArchiveCopyFn: func(context.Context, string, int64, int, int64, int, time.Time) error {
			return nil
		},
	}

	var archiveText string
	tg := &mockTelegram{
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			archiveText = text
			return 22, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessMessage(context.Background(), &models.Message{
		ID:                   1,
		BusinessConnectionID: "bc-text",
		Chat:                 models.Chat{ID: 1001},
		From:                 &models.User{ID: 77, Username: "Alice", FirstName: "Alice"},
		Text:                 "hello",
		Date:                 int(time.Now().Unix()),
	})

	if inserted == nil {
		t.Fatalf("expected archived record to be inserted")
	}
	if inserted.SourceUsername != "Alice" {
		t.Fatalf("expected source username Alice, got %q", inserted.SourceUsername)
	}
	if inserted.SourceDisplayName != "Alice" {
		t.Fatalf("expected source display name Alice, got %q", inserted.SourceDisplayName)
	}
	if inserted.SourceFromID == nil || *inserted.SourceFromID != 77 {
		t.Fatalf("expected source from id 77")
	}
	if !strings.Contains(archiveText, "source_username: @Alice") {
		t.Fatalf("expected archive header to include source username, got %q", archiveText)
	}
}

func TestHandleBusinessMessage_StoresSourceUsernameForMedia(t *testing.T) {
	logger := newTestLogger(t)
	var inserted *storage.ArchivedMessage
	voiceSent := 0

	repo := &mockRepo{
		insertIfNotExistsFn: func(_ context.Context, message *storage.ArchivedMessage) (bool, error) {
			copyMessage := *message
			inserted = &copyMessage
			return true, nil
		},
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   11,
				BusinessConnectionID: "bc-voice",
				SourceChatID:         2002,
				SourceMessageID:      2,
				CreatedAt:            time.Now().UTC(),
			}, nil
		},
		insertVersionFn: func(context.Context, *storage.MessageVersion) (int64, error) { return 1, nil },
		setArchiveCopyFn: func(context.Context, string, int64, int, int64, int, time.Time) error {
			return nil
		},
	}

	tg := &mockTelegram{
		sendVoiceByFileIDFn: func(context.Context, int64, string, string) (int, error) {
			voiceSent++
			return 33, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessMessage(context.Background(), &models.Message{
		ID:                   2,
		BusinessConnectionID: "bc-voice",
		Chat:                 models.Chat{ID: 2002},
		From:                 &models.User{ID: 88, Username: "VoiceUser", FirstName: "Voice"},
		Voice:                &models.Voice{FileID: "voice-id"},
		Date:                 int(time.Now().Unix()),
	})

	if inserted == nil {
		t.Fatalf("expected archived record to be inserted")
	}
	if inserted.SourceUsername != "VoiceUser" {
		t.Fatalf("expected source username VoiceUser, got %q", inserted.SourceUsername)
	}
	if voiceSent != 1 {
		t.Fatalf("expected voice archive send once, got %d", voiceSent)
	}
}

func TestHandleBusinessMessage_StoresEmptyUsernameWhenUnavailable(t *testing.T) {
	logger := newTestLogger(t)
	var inserted *storage.ArchivedMessage

	repo := &mockRepo{
		insertIfNotExistsFn: func(_ context.Context, message *storage.ArchivedMessage) (bool, error) {
			copyMessage := *message
			inserted = &copyMessage
			return true, nil
		},
		getBySourceFn: func(context.Context, string, int64, int) (*storage.ArchivedMessage, error) {
			return &storage.ArchivedMessage{
				ID:                   12,
				BusinessConnectionID: "bc-no-user",
				SourceChatID:         3003,
				SourceMessageID:      3,
				CreatedAt:            time.Now().UTC(),
			}, nil
		},
		insertVersionFn: func(context.Context, *storage.MessageVersion) (int64, error) { return 1, nil },
		setArchiveCopyFn: func(context.Context, string, int64, int, int64, int, time.Time) error {
			return nil
		},
	}

	tg := &mockTelegram{
		sendMessageFn: func(context.Context, int64, string) (int, error) { return 44, nil },
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessMessage(context.Background(), &models.Message{
		ID:                   3,
		BusinessConnectionID: "bc-no-user",
		Chat:                 models.Chat{ID: 3003},
		From:                 &models.User{ID: 99, FirstName: "NoUsername"},
		Text:                 "no username",
		Date:                 int(time.Now().Unix()),
	})

	if inserted == nil {
		t.Fatalf("expected archived record to be inserted")
	}
	if inserted.SourceUsername != "" {
		t.Fatalf("expected empty source username, got %q", inserted.SourceUsername)
	}
}

func TestHandleOwnerCommand_SendKnownUser(t *testing.T) {
	logger := newTestLogger(t)
	lookups := make([]string, 0, 1)
	sendBusinessCalls := 0
	var summary string

	repo := &mockRepo{
		findLatestChatTargetByUsernameFn: func(_ context.Context, normalizedUsername string) (*storage.BusinessSendTarget, error) {
			lookups = append(lookups, normalizedUsername)
			return &storage.BusinessSendTarget{
				BusinessConnectionID: "bc-send",
				TargetChatID:         5005,
			}, nil
		},
		getBusinessConnectionFn: func(context.Context, string) (*storage.BusinessConnection, error) {
			return &storage.BusinessConnection{
				ID:        "bc-send",
				IsEnabled: true,
				CanReply:  true,
			}, nil
		},
	}
	tg := &mockTelegram{
		sendBusinessMessageFn: func(context.Context, string, int64, string) (int, error) {
			sendBusinessCalls++
			return 55, nil
		},
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			summary = text
			return 56, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: 12345},
		From: &models.User{ID: 12345},
		Text: "/send [@KnownUser] [hello]",
	})

	if !handled {
		t.Fatalf("expected /send command to be handled")
	}
	if len(lookups) != 1 || lookups[0] != "knownuser" {
		t.Fatalf("expected lookup for normalized username knownuser, got %v", lookups)
	}
	if sendBusinessCalls != 1 {
		t.Fatalf("expected one business send call, got %d", sendBusinessCalls)
	}
	if !strings.Contains(summary, "Success: @knownuser") {
		t.Fatalf("expected success summary, got %q", summary)
	}
}

func TestHandleOwnerCommand_SendKnownAndUnknown(t *testing.T) {
	logger := newTestLogger(t)
	sendBusinessCalls := 0
	var summary string

	repo := &mockRepo{
		findLatestChatTargetByUsernameFn: func(_ context.Context, normalizedUsername string) (*storage.BusinessSendTarget, error) {
			if normalizedUsername == "knownuser" {
				return &storage.BusinessSendTarget{
					BusinessConnectionID: "bc-send",
					TargetChatID:         5006,
				}, nil
			}
			return nil, nil
		},
		getBusinessConnectionFn: func(context.Context, string) (*storage.BusinessConnection, error) {
			return &storage.BusinessConnection{
				ID:        "bc-send",
				IsEnabled: true,
				CanReply:  true,
			}, nil
		},
	}
	tg := &mockTelegram{
		sendBusinessMessageFn: func(context.Context, string, int64, string) (int, error) {
			sendBusinessCalls++
			return 57, nil
		},
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			summary = text
			return 58, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: 12345},
		From: &models.User{ID: 12345},
		Text: "/send [@knownuser, @unknownuser] [hello]",
	})

	if !handled {
		t.Fatalf("expected /send command to be handled")
	}
	if sendBusinessCalls != 1 {
		t.Fatalf("expected one business send call, got %d", sendBusinessCalls)
	}
	if !strings.Contains(summary, "Success: @knownuser") {
		t.Fatalf("expected known user in success list, got %q", summary)
	}
	if !strings.Contains(summary, "Unknown: @unknownuser") {
		t.Fatalf("expected unknown user in unknown list, got %q", summary)
	}
}

func TestHandleOwnerCommand_SendBlocksDisabledConnection(t *testing.T) {
	logger := newTestLogger(t)
	sendBusinessCalls := 0
	var summary string

	repo := &mockRepo{
		findLatestChatTargetByUsernameFn: func(context.Context, string) (*storage.BusinessSendTarget, error) {
			return &storage.BusinessSendTarget{
				BusinessConnectionID: "bc-disabled",
				TargetChatID:         5007,
			}, nil
		},
		getBusinessConnectionFn: func(context.Context, string) (*storage.BusinessConnection, error) {
			return &storage.BusinessConnection{
				ID:        "bc-disabled",
				IsEnabled: false,
				CanReply:  true,
			}, nil
		},
	}
	tg := &mockTelegram{
		sendBusinessMessageFn: func(context.Context, string, int64, string) (int, error) {
			sendBusinessCalls++
			return 0, nil
		},
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			summary = text
			return 60, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: 12345},
		From: &models.User{ID: 12345},
		Text: "/send [@alice] [hello]",
	})

	if !handled {
		t.Fatalf("expected /send command to be handled")
	}
	if sendBusinessCalls != 0 {
		t.Fatalf("disabled connection must block business send, got %d calls", sendBusinessCalls)
	}
	if !strings.Contains(summary, "business connection is disabled") {
		t.Fatalf("expected disabled connection reason, got %q", summary)
	}
}

func TestHandleOwnerCommand_SendBlocksNoReplyConnection(t *testing.T) {
	logger := newTestLogger(t)
	sendBusinessCalls := 0
	var summary string

	repo := &mockRepo{
		findLatestChatTargetByUsernameFn: func(context.Context, string) (*storage.BusinessSendTarget, error) {
			return &storage.BusinessSendTarget{
				BusinessConnectionID: "bc-no-reply",
				TargetChatID:         5008,
			}, nil
		},
		getBusinessConnectionFn: func(context.Context, string) (*storage.BusinessConnection, error) {
			return &storage.BusinessConnection{
				ID:        "bc-no-reply",
				IsEnabled: true,
				CanReply:  false,
			}, nil
		},
	}
	tg := &mockTelegram{
		sendBusinessMessageFn: func(context.Context, string, int64, string) (int, error) {
			sendBusinessCalls++
			return 0, nil
		},
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			summary = text
			return 61, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: 12345},
		From: &models.User{ID: 12345},
		Text: "/send [@alice] [hello]",
	})

	if !handled {
		t.Fatalf("expected /send command to be handled")
	}
	if sendBusinessCalls != 0 {
		t.Fatalf("can_reply=false must block business send, got %d calls", sendBusinessCalls)
	}
	if !strings.Contains(summary, "can_reply=false") {
		t.Fatalf("expected can_reply reason, got %q", summary)
	}
}

func TestHandleOwnerCommand_SendAllowsUnknownConnection(t *testing.T) {
	logger := newTestLogger(t)
	sendBusinessCalls := 0
	var summary string

	repo := &mockRepo{
		findLatestChatTargetByUsernameFn: func(context.Context, string) (*storage.BusinessSendTarget, error) {
			return &storage.BusinessSendTarget{
				BusinessConnectionID: "bc-unknown",
				TargetChatID:         5009,
			}, nil
		},
		getBusinessConnectionFn: func(context.Context, string) (*storage.BusinessConnection, error) {
			return nil, nil
		},
	}
	tg := &mockTelegram{
		sendBusinessMessageFn: func(context.Context, string, int64, string) (int, error) {
			sendBusinessCalls++
			return 0, nil
		},
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			summary = text
			return 62, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: 12345},
		From: &models.User{ID: 12345},
		Text: "/send [@alice] [hello]",
	})

	if !handled {
		t.Fatalf("expected /send command to be handled")
	}
	if sendBusinessCalls != 1 {
		t.Fatalf("unknown connection should still try business send, got %d calls", sendBusinessCalls)
	}
	if !strings.Contains(summary, "Success: @alice") {
		t.Fatalf("expected successful send summary, got %q", summary)
	}
}

func TestHandleOwnerCommand_InvalidSyntax(t *testing.T) {
	logger := newTestLogger(t)
	sendBusinessCalls := 0
	var summary string

	repo := &mockRepo{}
	tg := &mockTelegram{
		sendBusinessMessageFn: func(context.Context, string, int64, string) (int, error) {
			sendBusinessCalls++
			return 0, nil
		},
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			summary = text
			return 59, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: 12345},
		From: &models.User{ID: 12345},
		Text: "/send [@alice] []",
	})

	if !handled {
		t.Fatalf("expected /send command to be handled")
	}
	if sendBusinessCalls != 0 {
		t.Fatalf("expected no business send calls, got %d", sendBusinessCalls)
	}
	if summary != sendCommandUsage {
		t.Fatalf("expected usage summary, got %q", summary)
	}
}

func TestParseSendCommand(t *testing.T) {
	request, err := parseSendCommand("/send [@Alice, bob] [Meeting moved to 18:00]")
	if err != nil {
		t.Fatalf("parse valid command failed: %v", err)
	}
	if request.Text != "Meeting moved to 18:00" {
		t.Fatalf("unexpected text: %q", request.Text)
	}
	if len(request.Recipients) != 2 || request.Recipients[0] != "alice" || request.Recipients[1] != "bob" {
		t.Fatalf("unexpected recipients: %v", request.Recipients)
	}

	if _, err := parseSendCommand("/send [@alice]"); err == nil {
		t.Fatalf("expected parse error for missing message block")
	}
}

func TestBuildDeleteNotification_IncludesSourceUsername(t *testing.T) {
	logger := newTestLogger(t)
	svc := newTestService(t, logger, &mockRepo{}, &mockTelegram{})
	record := &storage.ArchivedMessage{
		SourceUsername:    "alice",
		SourceDisplayName: "Alice A",
		SourceChatID:      6006,
		SourceMessageID:   77,
		ContentType:       ContentTypeVoice,
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(time.Hour),
	}

	text := svc.buildDeleteNotification(record)
	if !strings.Contains(text, "From: <code>@alice</code>") {
		t.Fatalf("expected source username in delete notification, got %q", text)
	}
	if !strings.Contains(text, "Source chat ID: <code>6006</code>") {
		t.Fatalf("expected source chat id in delete notification, got %q", text)
	}
	if !strings.Contains(text, "Source message ID: <code>77</code>") {
		t.Fatalf("expected source message id in delete notification, got %q", text)
	}
}
