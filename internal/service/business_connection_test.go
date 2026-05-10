package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"removed-messages/internal/storage"
)

func TestHandleBusinessConnection_NewConnectionUpserts(t *testing.T) {
	logger := newTestLogger(t)

	var stored *storage.BusinessConnection
	var ownerNotification string

	repo := &mockRepo{
		getBusinessConnectionFn: func(context.Context, string) (*storage.BusinessConnection, error) {
			return nil, nil
		},
		upsertBusinessConnectionFn: func(_ context.Context, connection *storage.BusinessConnection) error {
			cp := *connection
			stored = &cp
			return nil
		},
	}
	tg := &mockTelegram{
		sendOwnerNotificationFn: func(_ context.Context, _ int64, text string) error {
			ownerNotification = text
			return nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessConnection(context.Background(), &models.BusinessConnection{
		ID:         "bc-new",
		User:       models.User{ID: 7777, Username: "Owner", FirstName: "Owner"},
		UserChatID: 7777,
		Date:       time.Now().Unix(),
		IsEnabled:  true,
		Rights:     &models.BusinessBotRights{CanReply: true},
	})

	if stored == nil {
		t.Fatalf("expected business connection to be persisted")
	}
	if stored.ID != "bc-new" {
		t.Fatalf("expected stored id bc-new, got %q", stored.ID)
	}
	if !stored.IsEnabled || !stored.CanReply {
		t.Fatalf("expected enabled+can_reply, got is_enabled=%t can_reply=%t", stored.IsEnabled, stored.CanReply)
	}
	if stored.OwnerUsername != "Owner" {
		t.Fatalf("expected owner username 'Owner', got %q", stored.OwnerUsername)
	}
	if !strings.Contains(ownerNotification, "Business connection established") {
		t.Fatalf("expected establish notification, got %q", ownerNotification)
	}
}

func TestHandleBusinessConnection_DisableAfterEnable(t *testing.T) {
	logger := newTestLogger(t)

	var ownerNotification string
	repo := &mockRepo{
		getBusinessConnectionFn: func(context.Context, string) (*storage.BusinessConnection, error) {
			return &storage.BusinessConnection{
				ID:          "bc-toggle",
				IsEnabled:   true,
				CanReply:    true,
				ConnectedAt: time.Now().UTC().Add(-time.Hour),
				UpdatedAt:   time.Now().UTC().Add(-time.Hour),
			}, nil
		},
		upsertBusinessConnectionFn: func(_ context.Context, connection *storage.BusinessConnection) error {
			if connection.IsEnabled {
				t.Fatalf("expected stored connection to be disabled")
			}
			if connection.DisconnectedAt == nil {
				t.Fatalf("expected disconnected_at to be set when disabling")
			}
			return nil
		},
	}
	tg := &mockTelegram{
		sendOwnerNotificationFn: func(_ context.Context, _ int64, text string) error {
			ownerNotification = text
			return nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessConnection(context.Background(), &models.BusinessConnection{
		ID:         "bc-toggle",
		User:       models.User{ID: 1, Username: "Owner"},
		UserChatID: 1,
		IsEnabled:  false,
	})

	if !strings.Contains(ownerNotification, "Business connection disabled") {
		t.Fatalf("expected disabled notification, got %q", ownerNotification)
	}
}

func TestHandleBusinessConnection_NoNotificationWhenStateUnchanged(t *testing.T) {
	logger := newTestLogger(t)

	notificationsSent := 0
	repo := &mockRepo{
		getBusinessConnectionFn: func(context.Context, string) (*storage.BusinessConnection, error) {
			return &storage.BusinessConnection{
				ID:        "bc-stable",
				IsEnabled: true,
				CanReply:  true,
			}, nil
		},
		upsertBusinessConnectionFn: func(context.Context, *storage.BusinessConnection) error { return nil },
	}
	tg := &mockTelegram{
		sendOwnerNotificationFn: func(context.Context, int64, string) error {
			notificationsSent++
			return nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	svc.HandleBusinessConnection(context.Background(), &models.BusinessConnection{
		ID:         "bc-stable",
		User:       models.User{ID: 1, Username: "Owner"},
		UserChatID: 1,
		IsEnabled:  true,
		Rights:     &models.BusinessBotRights{CanReply: true},
	})

	if notificationsSent != 0 {
		t.Fatalf("expected no owner notification when state unchanged, got %d", notificationsSent)
	}
}

func TestHandleOwnerCommand_ConnectionsListsKnown(t *testing.T) {
	logger := newTestLogger(t)
	var summary string

	repo := &mockRepo{
		listBusinessConnectionsFn: func(context.Context, bool) ([]storage.BusinessConnection, error) {
			return []storage.BusinessConnection{
				{
					ID:            "bc-a",
					OwnerUserID:   1,
					OwnerUsername: "alice",
					IsEnabled:     true,
					CanReply:      true,
					UpdatedAt:     time.Now().UTC(),
				},
				{
					ID:            "bc-b",
					OwnerUserID:   2,
					OwnerUsername: "bob",
					IsEnabled:     false,
					CanReply:      false,
					UpdatedAt:     time.Now().UTC(),
				},
			}, nil
		},
	}
	tg := &mockTelegram{
		sendMessageFn: func(_ context.Context, _ int64, text string) (int, error) {
			summary = text
			return 1, nil
		},
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: 12345},
		From: &models.User{ID: 12345},
		Text: "/connections",
	})

	if !handled {
		t.Fatalf("expected /connections to be handled")
	}
	if !strings.Contains(summary, "bc-a") || !strings.Contains(summary, "@alice") {
		t.Fatalf("expected summary to include enabled connection, got %q", summary)
	}
	if !strings.Contains(summary, "bc-b") {
		t.Fatalf("expected summary to include disabled connection too, got %q", summary)
	}
}

func TestHandleOwnerCommand_ConnectionsRequiresOwner(t *testing.T) {
	logger := newTestLogger(t)
	listed := 0
	repo := &mockRepo{
		listBusinessConnectionsFn: func(context.Context, bool) ([]storage.BusinessConnection, error) {
			listed++
			return nil, nil
		},
	}
	tg := &mockTelegram{
		sendMessageFn: func(context.Context, int64, string) (int, error) { return 1, nil },
	}

	svc := newTestService(t, logger, repo, tg)
	handled := svc.HandleOwnerCommand(context.Background(), &models.Message{
		Chat: models.Chat{ID: -123},
		From: &models.User{ID: 999}, // not the owner
		Text: "/connections",
	})

	if !handled {
		t.Fatalf("expected /connections to be handled with rejection")
	}
	if listed != 0 {
		t.Fatalf("non-owner must not see connections, got %d list calls", listed)
	}
}
