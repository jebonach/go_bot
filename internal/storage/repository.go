package storage

import (
	"context"
	"time"
)

type ArchivedMessage struct {
	ID                   int64
	BusinessConnectionID string
	SourceChatID         int64
	SourceMessageID      int
	SourceFromID         *int64
	SourceUsername       string
	SourceDisplayName    string
	ArchiveChatID        *int64
	ArchiveMessageID     *int
	OwnerChatID          int64
	MessageKind          string
	ContentType          string
	MediaGroupID         string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ExpiresAt            time.Time
	DeletedAt            *time.Time
	DeletionNotifiedAt   *time.Time
	ResentToOwnerAt      *time.Time
}

type MessageVersion struct {
	ID               int64
	ParentMessageID  int64
	VersionNo        int
	ContentType      string
	ArchiveMessageID *int
	EditDate         *int64
	CreatedAt        time.Time
}

type ArchiveCopy struct {
	ID                 int64
	ParentMessageID    int64
	VersionID          int64
	VersionNo          int
	ArchiveChatID      int64
	ArchiveMessageID   *int
	MetadataMessageID  *int
	SendStatus         string
	ErrorText          string
	SentAt             *time.Time
	DeletedFromArchive *time.Time
}

type BusinessSendTarget struct {
	BusinessConnectionID string
	TargetChatID         int64
	NormalizedUsername   string
	FirstSeenAt          time.Time
	UpdatedAt            time.Time
}

type BusinessConnection struct {
	ID               string
	OwnerUserID      int64
	OwnerUserChatID  int64
	OwnerUsername    string
	OwnerDisplayName string
	IsEnabled        bool
	CanReply         bool
	ConnectedAt      time.Time
	UpdatedAt        time.Time
	DisconnectedAt   *time.Time
}

type PendingDelete struct {
	ID                   int64
	BusinessConnectionID string
	SourceChatID         int64
	SourceMessageID      int
	FirstSeenAt          time.Time
	NextAttemptAt        time.Time
	AttemptCount         int
	Status               string
	LastError            string
	UpdatedAt            time.Time
}

type Repository interface {
	Migrate(ctx context.Context) error
	InsertIfNotExists(ctx context.Context, message *ArchivedMessage) (bool, error)
	SetArchiveCopy(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, archiveChatID int64, archiveMessageID int, updatedAt time.Time) error
	GetBySource(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*ArchivedMessage, error)
	UpsertBusinessTarget(ctx context.Context, target *BusinessSendTarget) error
	FindLatestChatTargetByUsername(ctx context.Context, normalizedUsername string) (*BusinessSendTarget, error)
	UpdateCurrentFromVersion(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, messageKind string, contentType string, archiveChatID *int64, archiveMessageID *int, updatedAt time.Time) error
	InsertVersion(ctx context.Context, version *MessageVersion) (int64, error)
	InsertNextVersion(ctx context.Context, version *MessageVersion) (int64, int, error)
	UpdateVersionArchiveMessageID(ctx context.Context, versionID int64, archiveMessageID *int) error
	GetLatestVersionByParentID(ctx context.Context, parentMessageID int64) (*MessageVersion, error)
	GetLatestVersionBySource(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*MessageVersion, error)
	GetVersionByParentAndNumber(ctx context.Context, parentMessageID int64, versionNo int) (*MessageVersion, error)
	InsertArchiveCopy(ctx context.Context, copy *ArchiveCopy) (int64, error)
	UpdateArchiveCopyOnSend(ctx context.Context, id int64, archiveMessageID *int, metadataMessageID *int, sentAt time.Time) error
	UpdateArchiveCopyOnFailure(ctx context.Context, id int64, errorText string) error
	ListArchiveCopiesByMessageID(ctx context.Context, parentMessageID int64) ([]ArchiveCopy, error)
	ListPendingArchiveCopiesOlderThan(ctx context.Context, threshold time.Time, limit int) ([]ArchiveCopy, error)
	MarkArchiveCopyDeleted(ctx context.Context, id int64, deletedAt time.Time) error
	MarkDeletedIfUnset(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, deletedAt time.Time) (bool, error)
	RecordDeleteProcessing(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, deletionNotifiedAt *time.Time, resentToOwnerAt *time.Time, updatedAt time.Time) error
	UpsertBusinessConnection(ctx context.Context, connection *BusinessConnection) error
	GetBusinessConnection(ctx context.Context, id string) (*BusinessConnection, error)
	ListBusinessConnections(ctx context.Context, onlyEnabled bool) ([]BusinessConnection, error)
	UpsertPendingDelete(ctx context.Context, pending *PendingDelete) error
	GetPendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*PendingDelete, error)
	ListDuePendingDeletes(ctx context.Context, now time.Time, limit int) ([]PendingDelete, error)
	DeletePendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) error
	ReschedulePendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, nextAttemptAt time.Time, attemptCount int, lastError string, updatedAt time.Time) error
	ListExpired(ctx context.Context, now time.Time, limit int) ([]ArchivedMessage, error)
	ListByMediaGroup(ctx context.Context, businessConnectionID string, sourceChatID int64, mediaGroupID string) ([]ArchivedMessage, error)
	DeleteByID(ctx context.Context, id int64) error
	Close() error
}
