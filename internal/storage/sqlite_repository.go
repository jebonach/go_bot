package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	dbTimeLayout      = "2006-01-02T15:04:05.000000000Z07:00"
	retryAttempts     = 5
	retryWaitDuration = 100 * time.Millisecond
)

const archivedMessagesDDL = `CREATE TABLE IF NOT EXISTS archived_messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	business_connection_id TEXT NOT NULL,
	source_chat_id INTEGER NOT NULL,
	source_message_id INTEGER NOT NULL,
	source_from_id INTEGER NULL,
	source_username TEXT NULL,
	source_display_name TEXT NULL,
	source_username_lc TEXT NULL,
	archive_chat_id INTEGER NULL,
	archive_message_id INTEGER NULL,
	owner_chat_id INTEGER NOT NULL,
	message_kind TEXT NOT NULL,
	content_type TEXT NOT NULL,
	media_group_id TEXT NULL,
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL,
	expires_at DATETIME NOT NULL,
	deleted_at DATETIME NULL,
	deletion_notified_at DATETIME NULL,
	resent_to_owner_at DATETIME NULL
);`

const messageVersionsDDL = `CREATE TABLE IF NOT EXISTS message_versions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	parent_message_id INTEGER NOT NULL,
	version_no INTEGER NOT NULL,
	content_type TEXT NOT NULL,
	archive_message_id INTEGER NULL,
	edit_date INTEGER NULL,
	created_at DATETIME NOT NULL,
	FOREIGN KEY(parent_message_id) REFERENCES archived_messages(id) ON DELETE CASCADE
);`

const businessTargetsDDL = `CREATE TABLE IF NOT EXISTS business_targets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	normalized_username TEXT NOT NULL,
	target_chat_id INTEGER NOT NULL,
	business_connection_id TEXT NOT NULL,
	first_seen_at DATETIME NOT NULL,
	last_seen_at DATETIME NOT NULL,
	UNIQUE(normalized_username)
);`

type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(path string) (*SQLiteRepository, error) {
	dbPath := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetConnMaxLifetime(0)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA foreign_keys=ON;",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set sqlite pragma %q: %w", pragma, err)
		}
	}

	return &SQLiteRepository{db: db}, nil
}

func (r *SQLiteRepository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *SQLiteRepository) Migrate(ctx context.Context) error {
	ddl := []string{
		archivedMessagesDDL,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_archived_messages_source_unique
			ON archived_messages (business_connection_id, source_chat_id, source_message_id);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_expires_at
			ON archived_messages (expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_deleted_at
			ON archived_messages (deleted_at);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_message_kind
			ON archived_messages (message_kind);`,
		messageVersionsDDL,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_message_versions_parent_version_unique
			ON message_versions (parent_message_id, version_no);`,
		`CREATE INDEX IF NOT EXISTS idx_message_versions_parent
			ON message_versions (parent_message_id);`,
		businessTargetsDDL,
		`CREATE INDEX IF NOT EXISTS idx_business_targets_last_seen
			ON business_targets (last_seen_at DESC, id DESC);`,
		`CREATE TABLE IF NOT EXISTS archive_copies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_message_id INTEGER NOT NULL,
			version_id INTEGER NULL,
			version_no INTEGER NOT NULL,
			archive_chat_id INTEGER NOT NULL,
			archive_message_id INTEGER NULL,
			metadata_message_id INTEGER NULL,
			send_status TEXT NOT NULL,
			error_text TEXT NULL,
			sent_at DATETIME NULL,
			deleted_from_archive_at DATETIME NULL,
			FOREIGN KEY(parent_message_id) REFERENCES archived_messages(id) ON DELETE CASCADE,
			FOREIGN KEY(version_id) REFERENCES message_versions(id) ON DELETE SET NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_archive_copies_parent
			ON archive_copies (parent_message_id);`,
		`CREATE TABLE IF NOT EXISTS business_connections (
			business_connection_id TEXT PRIMARY KEY,
			owner_user_id INTEGER NOT NULL,
			owner_user_chat_id INTEGER NOT NULL,
			owner_username TEXT NULL,
			owner_display_name TEXT NULL,
			is_enabled INTEGER NOT NULL,
			can_reply INTEGER NOT NULL,
			connected_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			disconnected_at DATETIME NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_business_connections_enabled
			ON business_connections (is_enabled, updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS pending_deletes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			business_connection_id TEXT NOT NULL,
			source_chat_id INTEGER NOT NULL,
			source_message_id INTEGER NOT NULL,
			first_seen_at DATETIME NOT NULL,
			next_attempt_at DATETIME NOT NULL,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			last_error TEXT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(business_connection_id, source_chat_id, source_message_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_pending_deletes_due
			ON pending_deletes (status, next_attempt_at);`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL
		);`,
	}

	for _, query := range ddl {
		q := query
		if err := r.withRetry(ctx, func() error {
			_, err := r.db.ExecContext(ctx, q)
			if err != nil {
				return fmt.Errorf("exec migration query: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
	}

	// Additive migration for installations created before sender identity fields existed.
	if err := r.ensureArchivedMessagesColumn(ctx, "source_from_id", "INTEGER NULL"); err != nil {
		return err
	}
	if err := r.ensureArchivedMessagesColumn(ctx, "source_username", "TEXT NULL"); err != nil {
		return err
	}
	if err := r.ensureArchivedMessagesColumn(ctx, "source_display_name", "TEXT NULL"); err != nil {
		return err
	}
	if err := r.ensureArchivedMessagesColumn(ctx, "source_username_lc", "TEXT NULL"); err != nil {
		return err
	}
	if err := r.ensureArchivedMessagesColumn(ctx, "media_group_id", "TEXT NULL"); err != nil {
		return err
	}
	if err := r.ensureTableColumn(ctx, "message_versions", "edit_date", "INTEGER NULL"); err != nil {
		return err
	}
	if err := r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_archived_messages_source_username
			ON archived_messages (source_username COLLATE NOCASE, updated_at DESC, id DESC);`)
		if err != nil {
			return fmt.Errorf("create source username index: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_archived_messages_source_username_lc
			ON archived_messages (source_username_lc, updated_at DESC, id DESC);`)
		if err != nil {
			return fmt.Errorf("create source username lc index: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_archive_copies_pending
			ON archive_copies (send_status, sent_at);`)
		if err != nil {
			return fmt.Errorf("create archive copies pending index: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_archived_messages_media_group
			ON archived_messages (business_connection_id, source_chat_id, media_group_id);`)
		if err != nil {
			return fmt.Errorf("create media group index: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	// Backfill source_username_lc for legacy rows so the indexed lookup works without TRIM/LOWER on column.
	if err := r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(ctx, `UPDATE archived_messages
			SET source_username_lc = LOWER(TRIM(source_username))
			WHERE source_username_lc IS NULL AND source_username IS NOT NULL AND TRIM(source_username) <> '';`)
		if err != nil {
			return fmt.Errorf("backfill source_username_lc: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.ensureTableColumn(ctx, "archive_copies", "metadata_message_id", "INTEGER NULL"); err != nil {
		return err
	}
	if err := r.ensureTableColumn(ctx, "archive_copies", "error_text", "TEXT NULL"); err != nil {
		return err
	}
	if err := r.ensureTableColumn(ctx, "archive_copies", "deleted_from_archive_at", "DATETIME NULL"); err != nil {
		return err
	}

	rebuilt, err := r.removeLegacyColumns(ctx)
	if err != nil {
		return err
	}
	if rebuilt {
		if err := r.recreateIndexes(ctx); err != nil {
			return err
		}
		if err := r.purgeSQLiteFreePages(ctx); err != nil {
			return err
		}
	}

	if err := r.recordSchemaMigration(ctx, currentSchemaVersion); err != nil {
		return err
	}

	return nil
}

const currentSchemaVersion = 3

func (r *SQLiteRepository) recordSchemaMigration(ctx context.Context, version int) error {
	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(
			ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)
				ON CONFLICT(version) DO UPDATE SET applied_at = excluded.applied_at;`,
			version,
			toDBTime(time.Now().UTC()),
		)
		if err != nil {
			return fmt.Errorf("record schema migration: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) InsertIfNotExists(ctx context.Context, message *ArchivedMessage) (bool, error) {
	if message == nil {
		return false, fmt.Errorf("message is nil")
	}

	const query = `INSERT INTO archived_messages (
		business_connection_id,
		source_chat_id,
		source_message_id,
		source_from_id,
		source_username,
		source_username_lc,
		source_display_name,
		archive_chat_id,
		archive_message_id,
		owner_chat_id,
		message_kind,
		content_type,
		media_group_id,
		created_at,
		updated_at,
		expires_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(business_connection_id, source_chat_id, source_message_id) DO NOTHING;`

	var inserted bool
	err := r.withRetry(ctx, func() error {
		result, err := r.db.ExecContext(
			ctx,
			query,
			message.BusinessConnectionID,
			message.SourceChatID,
			message.SourceMessageID,
			message.SourceFromID,
			nullableString(message.SourceUsername),
			nullableString(strings.ToLower(strings.TrimSpace(message.SourceUsername))),
			nullableString(message.SourceDisplayName),
			message.ArchiveChatID,
			message.ArchiveMessageID,
			message.OwnerChatID,
			message.MessageKind,
			message.ContentType,
			nullableString(message.MediaGroupID),
			toDBTime(message.CreatedAt),
			toDBTime(message.UpdatedAt),
			toDBTime(message.ExpiresAt),
		)
		if err != nil {
			return fmt.Errorf("insert archived message: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("insert archived message rows affected: %w", err)
		}

		inserted = rowsAffected > 0
		return nil
	})
	if err != nil {
		return false, err
	}

	return inserted, nil
}

func (r *SQLiteRepository) SetArchiveCopy(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, archiveChatID int64, archiveMessageID int, updatedAt time.Time) error {
	const query = `UPDATE archived_messages
		SET archive_chat_id = ?, archive_message_id = ?, updated_at = ?
		WHERE business_connection_id = ? AND source_chat_id = ? AND source_message_id = ?;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(
			ctx,
			query,
			archiveChatID,
			archiveMessageID,
			toDBTime(updatedAt),
			businessConnectionID,
			sourceChatID,
			sourceMessageID,
		)
		if err != nil {
			return fmt.Errorf("set archive copy: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) GetBySource(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*ArchivedMessage, error) {
	const query = `SELECT
		id,
		business_connection_id,
		source_chat_id,
		source_message_id,
		source_from_id,
		source_username,
		source_display_name,
		archive_chat_id,
		archive_message_id,
		owner_chat_id,
		message_kind,
		content_type,
		media_group_id,
		CAST(created_at AS TEXT),
		CAST(updated_at AS TEXT),
		CAST(expires_at AS TEXT),
		CAST(deleted_at AS TEXT),
		CAST(deletion_notified_at AS TEXT),
		CAST(resent_to_owner_at AS TEXT)
	FROM archived_messages
	WHERE business_connection_id = ? AND source_chat_id = ? AND source_message_id = ?
	LIMIT 1;`

	var output *ArchivedMessage
	err := r.withRetry(ctx, func() error {
		row := r.db.QueryRowContext(ctx, query, businessConnectionID, sourceChatID, sourceMessageID)

		message, err := scanArchivedMessage(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				output = nil
				return nil
			}
			return fmt.Errorf("query archived message by source: %w", err)
		}

		output = message
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) UpsertBusinessTarget(ctx context.Context, target *BusinessSendTarget) error {
	if target == nil {
		return fmt.Errorf("target is nil")
	}

	normalizedUsername := strings.TrimSpace(strings.ToLower(target.NormalizedUsername))
	if normalizedUsername == "" {
		return nil
	}

	updatedAt := target.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	firstSeenAt := target.FirstSeenAt
	if firstSeenAt.IsZero() {
		firstSeenAt = updatedAt
	}

	const query = `INSERT INTO business_targets (
		normalized_username,
		target_chat_id,
		business_connection_id,
		first_seen_at,
		last_seen_at
	) VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(normalized_username) DO UPDATE SET
		target_chat_id = excluded.target_chat_id,
		business_connection_id = excluded.business_connection_id,
		last_seen_at = excluded.last_seen_at;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(
			ctx,
			query,
			normalizedUsername,
			target.TargetChatID,
			target.BusinessConnectionID,
			toDBTime(firstSeenAt),
			toDBTime(updatedAt),
		)
		if err != nil {
			return fmt.Errorf("upsert business target: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) FindLatestChatTargetByUsername(ctx context.Context, normalizedUsername string) (*BusinessSendTarget, error) {
	normalizedUsername = strings.ToLower(strings.TrimSpace(normalizedUsername))
	if normalizedUsername == "" {
		return nil, nil
	}

	target, err := r.findBusinessTargetByUsername(ctx, normalizedUsername)
	if err != nil {
		return nil, err
	}
	if target != nil {
		return target, nil
	}

	return r.findLatestArchivedChatTargetByUsername(ctx, normalizedUsername)
}

func (r *SQLiteRepository) findBusinessTargetByUsername(ctx context.Context, normalizedUsername string) (*BusinessSendTarget, error) {
	const query = `SELECT
		business_connection_id,
		target_chat_id,
		normalized_username,
		CAST(first_seen_at AS TEXT),
		CAST(last_seen_at AS TEXT)
	FROM business_targets
	WHERE normalized_username = ? COLLATE NOCASE
	ORDER BY last_seen_at DESC, id DESC
	LIMIT 1;`

	var output *BusinessSendTarget
	err := r.withRetry(ctx, func() error {
		row := r.db.QueryRowContext(ctx, query, normalizedUsername)

		var (
			target         BusinessSendTarget
			normalizedRaw  sql.NullString
			firstSeenAtRaw string
			lastSeenAtRaw  string
		)

		if err := row.Scan(
			&target.BusinessConnectionID,
			&target.TargetChatID,
			&normalizedRaw,
			&firstSeenAtRaw,
			&lastSeenAtRaw,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				output = nil
				return nil
			}
			return fmt.Errorf("query business target by username: %w", err)
		}

		if normalizedRaw.Valid {
			target.NormalizedUsername = normalizedRaw.String
		}

		firstSeenAt, err := parseDBTime(firstSeenAtRaw)
		if err != nil {
			return fmt.Errorf("parse target first_seen_at: %w", err)
		}
		lastSeenAt, err := parseDBTime(lastSeenAtRaw)
		if err != nil {
			return fmt.Errorf("parse target last_seen_at: %w", err)
		}
		target.FirstSeenAt = firstSeenAt
		target.UpdatedAt = lastSeenAt
		output = &target
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) findLatestArchivedChatTargetByUsername(ctx context.Context, normalizedUsername string) (*BusinessSendTarget, error) {

	const query = `SELECT
		business_connection_id,
		source_chat_id,
		CAST(updated_at AS TEXT)
	FROM archived_messages
	WHERE source_username_lc = ?
		AND business_connection_id IS NOT NULL
		AND business_connection_id <> ''
		AND source_chat_id <> 0
	ORDER BY updated_at DESC, id DESC
	LIMIT 1;`

	var output *BusinessSendTarget
	err := r.withRetry(ctx, func() error {
		row := r.db.QueryRowContext(ctx, query, normalizedUsername)

		var (
			target       BusinessSendTarget
			updatedAtRaw string
		)

		if err := row.Scan(
			&target.BusinessConnectionID,
			&target.TargetChatID,
			&updatedAtRaw,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				output = nil
				return nil
			}
			return fmt.Errorf("query latest chat target by username: %w", err)
		}

		target.NormalizedUsername = strings.ToLower(strings.TrimSpace(normalizedUsername))

		updatedAt, err := parseDBTime(updatedAtRaw)
		if err != nil {
			return fmt.Errorf("parse target updated_at: %w", err)
		}
		target.UpdatedAt = updatedAt
		output = &target
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) UpdateCurrentFromVersion(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, messageKind string, contentType string, archiveChatID *int64, archiveMessageID *int, updatedAt time.Time) error {
	const query = `UPDATE archived_messages
		SET
			message_kind = ?,
			content_type = ?,
			updated_at = ?,
			archive_chat_id = CASE WHEN ? IS NULL THEN archive_chat_id ELSE ? END,
			archive_message_id = CASE WHEN ? IS NULL THEN archive_message_id ELSE ? END
		WHERE business_connection_id = ? AND source_chat_id = ? AND source_message_id = ?;`

	return r.withRetry(ctx, func() error {
		nullableArchiveChatID := nullableInt64(archiveChatID)
		nullableArchiveMessageID := nullableInt(archiveMessageID)

		_, err := r.db.ExecContext(
			ctx,
			query,
			messageKind,
			contentType,
			toDBTime(updatedAt),
			nullableArchiveChatID,
			nullableArchiveChatID,
			nullableArchiveMessageID,
			nullableArchiveMessageID,
			businessConnectionID,
			sourceChatID,
			sourceMessageID,
		)
		if err != nil {
			return fmt.Errorf("update current message from version: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) InsertVersion(ctx context.Context, version *MessageVersion) (int64, error) {
	if version == nil {
		return 0, fmt.Errorf("version is nil")
	}

	const query = `INSERT INTO message_versions (
		parent_message_id,
		version_no,
		content_type,
		archive_message_id,
		edit_date,
		created_at
	) VALUES (?, ?, ?, ?, ?, ?);`

	var insertedID int64
	err := r.withRetry(ctx, func() error {
		result, err := r.db.ExecContext(
			ctx,
			query,
			version.ParentMessageID,
			version.VersionNo,
			version.ContentType,
			nullableInt(version.ArchiveMessageID),
			nullableInt64(version.EditDate),
			toDBTime(version.CreatedAt),
		)
		if err != nil {
			return fmt.Errorf("insert message version: %w", err)
		}

		insertedID, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("insert message version last insert id: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	return insertedID, nil
}

// InsertNextVersion atomically computes the next version_no for a parent and inserts a row.
// Returns the inserted row id and the resolved version_no. The caller's version.VersionNo is ignored.
func (r *SQLiteRepository) InsertNextVersion(ctx context.Context, version *MessageVersion) (int64, int, error) {
	if version == nil {
		return 0, 0, fmt.Errorf("version is nil")
	}

	const query = `INSERT INTO message_versions (
		parent_message_id,
		version_no,
		content_type,
		archive_message_id,
		edit_date,
		created_at
	)
	SELECT
		?,
		COALESCE((SELECT MAX(version_no) FROM message_versions WHERE parent_message_id = ?), 0) + 1,
		?, ?, ?, ?
	RETURNING id, version_no;`

	var (
		insertedID  int64
		resolvedNo  int
	)
	err := r.withRetry(ctx, func() error {
		row := r.db.QueryRowContext(
			ctx,
			query,
			version.ParentMessageID,
			version.ParentMessageID,
			version.ContentType,
			nullableInt(version.ArchiveMessageID),
			nullableInt64(version.EditDate),
			toDBTime(version.CreatedAt),
		)
		if err := row.Scan(&insertedID, &resolvedNo); err != nil {
			return fmt.Errorf("insert next message version: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return insertedID, resolvedNo, nil
}

func (r *SQLiteRepository) UpdateVersionArchiveMessageID(ctx context.Context, versionID int64, archiveMessageID *int) error {
	const query = `UPDATE message_versions SET archive_message_id = ? WHERE id = ?;`
	return r.withRetry(ctx, func() error {
		if _, err := r.db.ExecContext(ctx, query, nullableInt(archiveMessageID), versionID); err != nil {
			return fmt.Errorf("update version archive message id: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) GetLatestVersionByParentID(ctx context.Context, parentMessageID int64) (*MessageVersion, error) {
	const query = `SELECT
		id,
		parent_message_id,
		version_no,
		content_type,
		archive_message_id,
		edit_date,
		CAST(created_at AS TEXT)
	FROM message_versions
	WHERE parent_message_id = ?
	ORDER BY version_no DESC
	LIMIT 1;`

	var output *MessageVersion
	err := r.withRetry(ctx, func() error {
		row := r.db.QueryRowContext(ctx, query, parentMessageID)
		version, err := scanMessageVersion(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				output = nil
				return nil
			}
			return fmt.Errorf("query latest version by parent id: %w", err)
		}
		output = version
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) GetLatestVersionBySource(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*MessageVersion, error) {
	parent, err := r.GetBySource(ctx, businessConnectionID, sourceChatID, sourceMessageID)
	if err != nil {
		return nil, fmt.Errorf("load parent for latest version by source: %w", err)
	}
	if parent == nil {
		return nil, nil
	}
	return r.GetLatestVersionByParentID(ctx, parent.ID)
}

func (r *SQLiteRepository) GetVersionByParentAndNumber(ctx context.Context, parentMessageID int64, versionNo int) (*MessageVersion, error) {
	const query = `SELECT
		id,
		parent_message_id,
		version_no,
		content_type,
		archive_message_id,
		edit_date,
		CAST(created_at AS TEXT)
	FROM message_versions
	WHERE parent_message_id = ? AND version_no = ?
	LIMIT 1;`

	var output *MessageVersion
	err := r.withRetry(ctx, func() error {
		row := r.db.QueryRowContext(ctx, query, parentMessageID, versionNo)
		version, err := scanMessageVersion(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				output = nil
				return nil
			}
			return fmt.Errorf("query version by parent and number: %w", err)
		}
		output = version
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) InsertArchiveCopy(ctx context.Context, copy *ArchiveCopy) (int64, error) {
	if copy == nil {
		return 0, fmt.Errorf("archive copy is nil")
	}

	const query = `INSERT INTO archive_copies (
		parent_message_id,
		version_id,
		version_no,
		archive_chat_id,
		archive_message_id,
		metadata_message_id,
		send_status,
		error_text,
		sent_at,
		deleted_from_archive_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`

	var insertedID int64
	err := r.withRetry(ctx, func() error {
		result, err := r.db.ExecContext(
			ctx,
			query,
			copy.ParentMessageID,
			nullableInt64(zeroNilInt64(copy.VersionID)),
			copy.VersionNo,
			copy.ArchiveChatID,
			nullableInt(copy.ArchiveMessageID),
			nullableInt(copy.MetadataMessageID),
			copy.SendStatus,
			nullableString(copy.ErrorText),
			nullableTime(copy.SentAt),
			nullableTime(copy.DeletedFromArchive),
		)
		if err != nil {
			return fmt.Errorf("insert archive copy: %w", err)
		}

		insertedID, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("insert archive copy last insert id: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	return insertedID, nil
}

func (r *SQLiteRepository) ListArchiveCopiesByMessageID(ctx context.Context, parentMessageID int64) ([]ArchiveCopy, error) {
	const query = `SELECT
		id,
		parent_message_id,
		version_id,
		version_no,
		archive_chat_id,
		archive_message_id,
		metadata_message_id,
		send_status,
		error_text,
		CAST(sent_at AS TEXT),
		CAST(deleted_from_archive_at AS TEXT)
	FROM archive_copies
	WHERE parent_message_id = ?
	ORDER BY version_no ASC, id ASC;`

	var output []ArchiveCopy
	err := r.withRetry(ctx, func() error {
		rows, err := r.db.QueryContext(ctx, query, parentMessageID)
		if err != nil {
			return fmt.Errorf("list archive copies by message id: %w", err)
		}
		defer rows.Close()

		copies := make([]ArchiveCopy, 0)
		for rows.Next() {
			copy, err := scanArchiveCopy(rows)
			if err != nil {
				return fmt.Errorf("scan archive copy: %w", err)
			}
			copies = append(copies, *copy)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate archive copies: %w", err)
		}
		output = copies
		return nil
	})
	if err != nil {
		return nil, err
	}

	legacyOutput, err := r.listLegacyArchiveCopiesByMessageID(ctx, parentMessageID)
	if err != nil {
		return nil, err
	}
	if len(legacyOutput) == 0 {
		return output, nil
	}
	if len(output) == 0 {
		return legacyOutput, nil
	}

	seen := make(map[string]struct{}, len(output))
	for _, copy := range output {
		seen[archiveCopyDedupeKey(copy)] = struct{}{}
	}
	for _, copy := range legacyOutput {
		key := archiveCopyDedupeKey(copy)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		output = append(output, copy)
	}

	return output, nil
}

func archiveCopyDedupeKey(copy ArchiveCopy) string {
	archiveMessageID := 0
	if copy.ArchiveMessageID != nil {
		archiveMessageID = *copy.ArchiveMessageID
	}
	return fmt.Sprintf("%d:%d:%d:%d", copy.ParentMessageID, copy.VersionNo, copy.ArchiveChatID, archiveMessageID)
}

func (r *SQLiteRepository) listLegacyArchiveCopiesByMessageID(ctx context.Context, parentMessageID int64) ([]ArchiveCopy, error) {
	const query = `SELECT
		v.parent_message_id,
		v.id,
		v.version_no,
		COALESCE(m.archive_chat_id, 0),
		v.archive_message_id,
		CAST(v.created_at AS TEXT)
	FROM message_versions v
	INNER JOIN archived_messages m ON m.id = v.parent_message_id
	WHERE v.parent_message_id = ? AND v.archive_message_id IS NOT NULL
	ORDER BY v.version_no ASC, v.id ASC;`

	var output []ArchiveCopy
	err := r.withRetry(ctx, func() error {
		rows, err := r.db.QueryContext(ctx, query, parentMessageID)
		if err != nil {
			return fmt.Errorf("list legacy archive copies by message id: %w", err)
		}
		defer rows.Close()

		copies := make([]ArchiveCopy, 0)
		for rows.Next() {
			var (
				copy                ArchiveCopy
				archiveMessageIDRaw sql.NullInt64
				sentAtRaw           string
			)
			if err := rows.Scan(
				&copy.ParentMessageID,
				&copy.VersionID,
				&copy.VersionNo,
				&copy.ArchiveChatID,
				&archiveMessageIDRaw,
				&sentAtRaw,
			); err != nil {
				return fmt.Errorf("scan legacy archive copy: %w", err)
			}
			if archiveMessageIDRaw.Valid {
				value := int(archiveMessageIDRaw.Int64)
				copy.ArchiveMessageID = &value
			}
			sentAt, err := parseDBTime(sentAtRaw)
			if err != nil {
				return fmt.Errorf("parse legacy archive copy sent_at: %w", err)
			}
			copy.SentAt = &sentAt
			copy.SendStatus = "sent"
			copies = append(copies, copy)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate legacy archive copies: %w", err)
		}
		output = copies
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) UpdateArchiveCopyOnSend(ctx context.Context, id int64, archiveMessageID *int, metadataMessageID *int, sentAt time.Time) error {
	const query = `UPDATE archive_copies
		SET archive_message_id = ?, metadata_message_id = ?, send_status = 'sent', error_text = NULL, sent_at = ?
		WHERE id = ?;`
	return r.withRetry(ctx, func() error {
		if _, err := r.db.ExecContext(
			ctx,
			query,
			nullableInt(archiveMessageID),
			nullableInt(metadataMessageID),
			toDBTime(sentAt),
			id,
		); err != nil {
			return fmt.Errorf("update archive copy on send: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) UpdateArchiveCopyOnFailure(ctx context.Context, id int64, errorText string) error {
	const query = `UPDATE archive_copies
		SET send_status = 'failed', error_text = ?
		WHERE id = ?;`
	return r.withRetry(ctx, func() error {
		if _, err := r.db.ExecContext(ctx, query, nullableString(errorText), id); err != nil {
			return fmt.Errorf("update archive copy on failure: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) ListPendingArchiveCopiesOlderThan(ctx context.Context, threshold time.Time, limit int) ([]ArchiveCopy, error) {
	if limit <= 0 {
		limit = 100
	}

	const query = `SELECT
		id,
		parent_message_id,
		version_id,
		version_no,
		archive_chat_id,
		archive_message_id,
		metadata_message_id,
		send_status,
		error_text,
		CAST(sent_at AS TEXT),
		CAST(deleted_from_archive_at AS TEXT)
	FROM archive_copies
	WHERE send_status = 'pending'
	ORDER BY id ASC
	LIMIT ?;`

	_ = threshold // archive_copies has no created_at; status='pending' rows are by definition unfinished.
	var output []ArchiveCopy
	err := r.withRetry(ctx, func() error {
		rows, err := r.db.QueryContext(ctx, query, limit)
		if err != nil {
			return fmt.Errorf("list pending archive copies: %w", err)
		}
		defer rows.Close()

		copies := make([]ArchiveCopy, 0)
		for rows.Next() {
			copy, err := scanArchiveCopy(rows)
			if err != nil {
				return fmt.Errorf("scan pending archive copy: %w", err)
			}
			copies = append(copies, *copy)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate pending archive copies: %w", err)
		}
		output = copies
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) ListByMediaGroup(ctx context.Context, businessConnectionID string, sourceChatID int64, mediaGroupID string) ([]ArchivedMessage, error) {
	if strings.TrimSpace(mediaGroupID) == "" {
		return nil, nil
	}

	const query = `SELECT
		id,
		business_connection_id,
		source_chat_id,
		source_message_id,
		source_from_id,
		source_username,
		source_display_name,
		archive_chat_id,
		archive_message_id,
		owner_chat_id,
		message_kind,
		content_type,
		media_group_id,
		CAST(created_at AS TEXT),
		CAST(updated_at AS TEXT),
		CAST(expires_at AS TEXT),
		CAST(deleted_at AS TEXT),
		CAST(deletion_notified_at AS TEXT),
		CAST(resent_to_owner_at AS TEXT)
	FROM archived_messages
	WHERE business_connection_id = ? AND source_chat_id = ? AND media_group_id = ?
	ORDER BY source_message_id ASC;`

	var output []ArchivedMessage
	err := r.withRetry(ctx, func() error {
		rows, err := r.db.QueryContext(ctx, query, businessConnectionID, sourceChatID, mediaGroupID)
		if err != nil {
			return fmt.Errorf("list by media group: %w", err)
		}
		defer rows.Close()

		records := make([]ArchivedMessage, 0)
		for rows.Next() {
			message, err := scanArchivedMessage(rows)
			if err != nil {
				return fmt.Errorf("scan media group record: %w", err)
			}
			records = append(records, *message)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate media group records: %w", err)
		}
		output = records
		return nil
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (r *SQLiteRepository) MarkArchiveCopyDeleted(ctx context.Context, id int64, deletedAt time.Time) error {
	const query = `UPDATE archive_copies
		SET deleted_from_archive_at = COALESCE(deleted_from_archive_at, ?)
		WHERE id = ?;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(ctx, query, toDBTime(deletedAt), id)
		if err != nil {
			return fmt.Errorf("mark archive copy deleted: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) MarkDeletedIfUnset(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, deletedAt time.Time) (bool, error) {
	const query = `UPDATE archived_messages
		SET deleted_at = ?, updated_at = ?
		WHERE business_connection_id = ? AND source_chat_id = ? AND source_message_id = ? AND deleted_at IS NULL;`

	var changed bool
	err := r.withRetry(ctx, func() error {
		result, err := r.db.ExecContext(
			ctx,
			query,
			toDBTime(deletedAt),
			toDBTime(deletedAt),
			businessConnectionID,
			sourceChatID,
			sourceMessageID,
		)
		if err != nil {
			return fmt.Errorf("mark deleted: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("mark deleted rows affected: %w", err)
		}

		changed = rowsAffected > 0
		return nil
	})
	if err != nil {
		return false, err
	}

	return changed, nil
}

func (r *SQLiteRepository) RecordDeleteProcessing(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, deletionNotifiedAt *time.Time, resentToOwnerAt *time.Time, updatedAt time.Time) error {
	const query = `UPDATE archived_messages
		SET
			deletion_notified_at = COALESCE(deletion_notified_at, ?),
			resent_to_owner_at = COALESCE(resent_to_owner_at, ?),
			updated_at = ?
		WHERE business_connection_id = ? AND source_chat_id = ? AND source_message_id = ?;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(
			ctx,
			query,
			nullableTime(deletionNotifiedAt),
			nullableTime(resentToOwnerAt),
			toDBTime(updatedAt),
			businessConnectionID,
			sourceChatID,
			sourceMessageID,
		)
		if err != nil {
			return fmt.Errorf("record delete processing: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) UpsertBusinessConnection(ctx context.Context, connection *BusinessConnection) error {
	if connection == nil {
		return fmt.Errorf("business connection is nil")
	}
	if strings.TrimSpace(connection.ID) == "" {
		return fmt.Errorf("business connection id is empty")
	}

	updatedAt := connection.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	connectedAt := connection.ConnectedAt
	if connectedAt.IsZero() {
		connectedAt = updatedAt
	}

	const query = `INSERT INTO business_connections (
		business_connection_id,
		owner_user_id,
		owner_user_chat_id,
		owner_username,
		owner_display_name,
		is_enabled,
		can_reply,
		connected_at,
		updated_at,
		disconnected_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(business_connection_id) DO UPDATE SET
		owner_user_id = excluded.owner_user_id,
		owner_user_chat_id = excluded.owner_user_chat_id,
		owner_username = excluded.owner_username,
		owner_display_name = excluded.owner_display_name,
		is_enabled = excluded.is_enabled,
		can_reply = excluded.can_reply,
		updated_at = excluded.updated_at,
		disconnected_at = excluded.disconnected_at;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(
			ctx,
			query,
			connection.ID,
			connection.OwnerUserID,
			connection.OwnerUserChatID,
			nullableString(connection.OwnerUsername),
			nullableString(connection.OwnerDisplayName),
			boolToInt(connection.IsEnabled),
			boolToInt(connection.CanReply),
			toDBTime(connectedAt),
			toDBTime(updatedAt),
			nullableTime(connection.DisconnectedAt),
		)
		if err != nil {
			return fmt.Errorf("upsert business connection: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) GetBusinessConnection(ctx context.Context, id string) (*BusinessConnection, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}

	const query = `SELECT
		business_connection_id,
		owner_user_id,
		owner_user_chat_id,
		owner_username,
		owner_display_name,
		is_enabled,
		can_reply,
		CAST(connected_at AS TEXT),
		CAST(updated_at AS TEXT),
		CAST(disconnected_at AS TEXT)
	FROM business_connections
	WHERE business_connection_id = ?
	LIMIT 1;`

	var output *BusinessConnection
	err := r.withRetry(ctx, func() error {
		row := r.db.QueryRowContext(ctx, query, id)
		conn, err := scanBusinessConnection(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				output = nil
				return nil
			}
			return fmt.Errorf("query business connection: %w", err)
		}
		output = conn
		return nil
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (r *SQLiteRepository) ListBusinessConnections(ctx context.Context, onlyEnabled bool) ([]BusinessConnection, error) {
	query := `SELECT
		business_connection_id,
		owner_user_id,
		owner_user_chat_id,
		owner_username,
		owner_display_name,
		is_enabled,
		can_reply,
		CAST(connected_at AS TEXT),
		CAST(updated_at AS TEXT),
		CAST(disconnected_at AS TEXT)
	FROM business_connections`
	if onlyEnabled {
		query += ` WHERE is_enabled = 1`
	}
	query += ` ORDER BY updated_at DESC, business_connection_id ASC;`

	var output []BusinessConnection
	err := r.withRetry(ctx, func() error {
		rows, err := r.db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("list business connections: %w", err)
		}
		defer rows.Close()

		records := make([]BusinessConnection, 0)
		for rows.Next() {
			conn, err := scanBusinessConnection(rows)
			if err != nil {
				return fmt.Errorf("scan business connection: %w", err)
			}
			records = append(records, *conn)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate business connections: %w", err)
		}
		output = records
		return nil
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

func scanBusinessConnection(scanner rowScanner) (*BusinessConnection, error) {
	var (
		conn               BusinessConnection
		ownerUsernameRaw   sql.NullString
		ownerDisplayName   sql.NullString
		isEnabledRaw       int
		canReplyRaw        int
		connectedAtRaw     string
		updatedAtRaw       string
		disconnectedAtRaw  sql.NullString
	)

	if err := scanner.Scan(
		&conn.ID,
		&conn.OwnerUserID,
		&conn.OwnerUserChatID,
		&ownerUsernameRaw,
		&ownerDisplayName,
		&isEnabledRaw,
		&canReplyRaw,
		&connectedAtRaw,
		&updatedAtRaw,
		&disconnectedAtRaw,
	); err != nil {
		return nil, err
	}

	if ownerUsernameRaw.Valid {
		conn.OwnerUsername = ownerUsernameRaw.String
	}
	if ownerDisplayName.Valid {
		conn.OwnerDisplayName = ownerDisplayName.String
	}
	conn.IsEnabled = isEnabledRaw != 0
	conn.CanReply = canReplyRaw != 0

	connectedAt, err := parseDBTime(connectedAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse business connection connected_at: %w", err)
	}
	conn.ConnectedAt = connectedAt

	updatedAt, err := parseDBTime(updatedAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse business connection updated_at: %w", err)
	}
	conn.UpdatedAt = updatedAt

	if disconnectedAtRaw.Valid {
		parsed, err := parseDBTime(disconnectedAtRaw.String)
		if err != nil {
			return nil, fmt.Errorf("parse business connection disconnected_at: %w", err)
		}
		conn.DisconnectedAt = &parsed
	}

	return &conn, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (r *SQLiteRepository) UpsertPendingDelete(ctx context.Context, pending *PendingDelete) error {
	if pending == nil {
		return fmt.Errorf("pending delete is nil")
	}

	now := pending.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	firstSeenAt := pending.FirstSeenAt
	if firstSeenAt.IsZero() {
		firstSeenAt = now
	}
	nextAttemptAt := pending.NextAttemptAt
	if nextAttemptAt.IsZero() {
		nextAttemptAt = now
	}
	status := strings.TrimSpace(pending.Status)
	if status == "" {
		status = "pending"
	}

	const query = `INSERT INTO pending_deletes (
		business_connection_id,
		source_chat_id,
		source_message_id,
		first_seen_at,
		next_attempt_at,
		attempt_count,
		status,
		last_error,
		updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(business_connection_id, source_chat_id, source_message_id) DO UPDATE SET
		next_attempt_at = excluded.next_attempt_at,
		status = excluded.status,
		last_error = COALESCE(excluded.last_error, pending_deletes.last_error),
		updated_at = excluded.updated_at;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(
			ctx,
			query,
			pending.BusinessConnectionID,
			pending.SourceChatID,
			pending.SourceMessageID,
			toDBTime(firstSeenAt),
			toDBTime(nextAttemptAt),
			pending.AttemptCount,
			status,
			nullableString(pending.LastError),
			toDBTime(now),
		)
		if err != nil {
			return fmt.Errorf("upsert pending delete: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) GetPendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) (*PendingDelete, error) {
	const query = `SELECT
		id,
		business_connection_id,
		source_chat_id,
		source_message_id,
		CAST(first_seen_at AS TEXT),
		CAST(next_attempt_at AS TEXT),
		attempt_count,
		status,
		last_error,
		CAST(updated_at AS TEXT)
	FROM pending_deletes
	WHERE business_connection_id = ? AND source_chat_id = ? AND source_message_id = ?
	LIMIT 1;`

	var output *PendingDelete
	err := r.withRetry(ctx, func() error {
		row := r.db.QueryRowContext(ctx, query, businessConnectionID, sourceChatID, sourceMessageID)
		pending, err := scanPendingDelete(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				output = nil
				return nil
			}
			return fmt.Errorf("query pending delete: %w", err)
		}
		output = pending
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) ListDuePendingDeletes(ctx context.Context, now time.Time, limit int) ([]PendingDelete, error) {
	if limit <= 0 {
		limit = 100
	}

	const query = `SELECT
		id,
		business_connection_id,
		source_chat_id,
		source_message_id,
		CAST(first_seen_at AS TEXT),
		CAST(next_attempt_at AS TEXT),
		attempt_count,
		status,
		last_error,
		CAST(updated_at AS TEXT)
	FROM pending_deletes
	WHERE status = 'pending' AND next_attempt_at <= ?
	ORDER BY next_attempt_at ASC, id ASC
	LIMIT ?;`

	var output []PendingDelete
	err := r.withRetry(ctx, func() error {
		rows, err := r.db.QueryContext(ctx, query, toDBTime(now), limit)
		if err != nil {
			return fmt.Errorf("list due pending deletes: %w", err)
		}
		defer rows.Close()

		items := make([]PendingDelete, 0, limit)
		for rows.Next() {
			pending, err := scanPendingDelete(rows)
			if err != nil {
				return fmt.Errorf("scan due pending delete: %w", err)
			}
			items = append(items, *pending)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate due pending deletes: %w", err)
		}
		output = items
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) DeletePendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int) error {
	const query = `DELETE FROM pending_deletes
		WHERE business_connection_id = ? AND source_chat_id = ? AND source_message_id = ?;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(ctx, query, businessConnectionID, sourceChatID, sourceMessageID)
		if err != nil {
			return fmt.Errorf("delete pending delete: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) ReschedulePendingDelete(ctx context.Context, businessConnectionID string, sourceChatID int64, sourceMessageID int, nextAttemptAt time.Time, attemptCount int, lastError string, updatedAt time.Time) error {
	const query = `UPDATE pending_deletes
		SET next_attempt_at = ?, attempt_count = ?, last_error = ?, updated_at = ?
		WHERE business_connection_id = ? AND source_chat_id = ? AND source_message_id = ?;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(
			ctx,
			query,
			toDBTime(nextAttemptAt),
			attemptCount,
			nullableString(lastError),
			toDBTime(updatedAt),
			businessConnectionID,
			sourceChatID,
			sourceMessageID,
		)
		if err != nil {
			return fmt.Errorf("reschedule pending delete: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) ListExpired(ctx context.Context, now time.Time, limit int) ([]ArchivedMessage, error) {
	if limit <= 0 {
		limit = 100
	}

	const query = `SELECT
		id,
		business_connection_id,
		source_chat_id,
		source_message_id,
		source_from_id,
		source_username,
		source_display_name,
		archive_chat_id,
		archive_message_id,
		owner_chat_id,
		message_kind,
		content_type,
		media_group_id,
		CAST(created_at AS TEXT),
		CAST(updated_at AS TEXT),
		CAST(expires_at AS TEXT),
		CAST(deleted_at AS TEXT),
		CAST(deletion_notified_at AS TEXT),
		CAST(resent_to_owner_at AS TEXT)
	FROM archived_messages
	WHERE expires_at <= ?
	ORDER BY expires_at ASC
	LIMIT ?;`

	var output []ArchivedMessage
	err := r.withRetry(ctx, func() error {
		rows, err := r.db.QueryContext(ctx, query, toDBTime(now), limit)
		if err != nil {
			return fmt.Errorf("list expired records: %w", err)
		}
		defer rows.Close()

		records := make([]ArchivedMessage, 0, limit)
		for rows.Next() {
			message, err := scanArchivedMessage(rows)
			if err != nil {
				return fmt.Errorf("scan expired record: %w", err)
			}
			records = append(records, *message)
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate expired records: %w", err)
		}

		output = records
		return nil
	})
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (r *SQLiteRepository) DeleteByID(ctx context.Context, id int64) error {
	const query = `DELETE FROM archived_messages WHERE id = ?;`

	return r.withRetry(ctx, func() error {
		_, err := r.db.ExecContext(ctx, query, id)
		if err != nil {
			return fmt.Errorf("delete archived message by id: %w", err)
		}
		return nil
	})
}

func (r *SQLiteRepository) removeLegacyColumns(ctx context.Context) (bool, error) {
	archivedColumns, err := r.tableColumns(ctx, "archived_messages")
	if err != nil {
		return false, err
	}
	versionColumns, err := r.tableColumns(ctx, "message_versions")
	if err != nil {
		return false, err
	}
	targetColumns, err := r.tableColumns(ctx, "business_targets")
	if err != nil {
		return false, err
	}

	rebuildArchivedMessages := hasAnyColumn(archivedColumns, "file_id", "file_unique_id", "text_preview", "caption")
	rebuildMessageVersions := hasAnyColumn(versionColumns, "text_full", "text_preview", "caption_full", "caption", "file_id", "file_unique_id", "metadata_json")
	rebuildBusinessTargets := hasAnyColumn(targetColumns, "source_username", "source_display_name", "source_from_id")
	if !rebuildArchivedMessages && !rebuildMessageVersions && !rebuildBusinessTargets {
		return false, nil
	}

	if err := r.withRetry(ctx, func() error {
		return r.rebuildLegacyColumnTables(ctx, rebuildArchivedMessages, rebuildMessageVersions, rebuildBusinessTargets)
	}); err != nil {
		return false, err
	}

	return true, nil
}

func (r *SQLiteRepository) rebuildLegacyColumnTables(ctx context.Context, rebuildArchivedMessages bool, rebuildMessageVersions bool, rebuildBusinessTargets bool) error {
	if _, err := r.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF;"); err != nil {
		return fmt.Errorf("disable sqlite foreign keys for legacy rebuild: %w", err)
	}
	foreignKeysRestored := false
	defer func() {
		if !foreignKeysRestored {
			_, _ = r.db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON;")
		}
	}()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy column rebuild: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if rebuildArchivedMessages {
		if err := rebuildArchivedMessagesTable(ctx, tx); err != nil {
			return err
		}
	}
	if rebuildMessageVersions {
		if err := rebuildMessageVersionsTable(ctx, tx); err != nil {
			return err
		}
	}
	if rebuildBusinessTargets {
		if err := rebuildBusinessTargetsTable(ctx, tx); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy column rebuild: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, "PRAGMA foreign_keys=ON;"); err != nil {
		return fmt.Errorf("restore sqlite foreign keys after legacy rebuild: %w", err)
	}
	foreignKeysRestored = true

	return nil
}

func rebuildArchivedMessagesTable(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP TABLE IF EXISTS archived_messages_new;`,
		strings.Replace(archivedMessagesDDL, "CREATE TABLE IF NOT EXISTS archived_messages", "CREATE TABLE archived_messages_new", 1),
		`INSERT INTO archived_messages_new (
			id,
			business_connection_id,
			source_chat_id,
			source_message_id,
			source_from_id,
			source_username,
			source_display_name,
			source_username_lc,
			archive_chat_id,
			archive_message_id,
			owner_chat_id,
			message_kind,
			content_type,
			media_group_id,
			created_at,
			updated_at,
			expires_at,
			deleted_at,
			deletion_notified_at,
			resent_to_owner_at
		)
		SELECT
			id,
			business_connection_id,
			source_chat_id,
			source_message_id,
			source_from_id,
			source_username,
			source_display_name,
			CASE WHEN source_username IS NULL OR TRIM(source_username) = '' THEN NULL ELSE LOWER(TRIM(source_username)) END,
			archive_chat_id,
			archive_message_id,
			owner_chat_id,
			message_kind,
			content_type,
			NULL,
			created_at,
			updated_at,
			expires_at,
			deleted_at,
			deletion_notified_at,
			resent_to_owner_at
		FROM archived_messages;`,
		`DROP TABLE archived_messages;`,
		`ALTER TABLE archived_messages_new RENAME TO archived_messages;`,
	}
	return execTxStatements(ctx, tx, "rebuild archived_messages", statements)
}

func rebuildMessageVersionsTable(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP TABLE IF EXISTS message_versions_new;`,
		strings.Replace(messageVersionsDDL, "CREATE TABLE IF NOT EXISTS message_versions", "CREATE TABLE message_versions_new", 1),
		`INSERT INTO message_versions_new (
			id,
			parent_message_id,
			version_no,
			content_type,
			archive_message_id,
			edit_date,
			created_at
		)
		SELECT
			id,
			parent_message_id,
			version_no,
			content_type,
			archive_message_id,
			NULL,
			created_at
		FROM message_versions;`,
		`DROP TABLE message_versions;`,
		`ALTER TABLE message_versions_new RENAME TO message_versions;`,
	}
	return execTxStatements(ctx, tx, "rebuild message_versions", statements)
}

func rebuildBusinessTargetsTable(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP TABLE IF EXISTS business_targets_new;`,
		strings.Replace(businessTargetsDDL, "CREATE TABLE IF NOT EXISTS business_targets", "CREATE TABLE business_targets_new", 1),
		`INSERT INTO business_targets_new (
			id,
			normalized_username,
			target_chat_id,
			business_connection_id,
			first_seen_at,
			last_seen_at
		)
		SELECT
			id,
			normalized_username,
			target_chat_id,
			business_connection_id,
			first_seen_at,
			last_seen_at
		FROM business_targets;`,
		`DROP TABLE business_targets;`,
		`ALTER TABLE business_targets_new RENAME TO business_targets;`,
	}
	return execTxStatements(ctx, tx, "rebuild business_targets", statements)
}

func execTxStatements(ctx context.Context, tx *sql.Tx, label string, statements []string) error {
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	return nil
}

func (r *SQLiteRepository) recreateIndexes(ctx context.Context) error {
	indexes := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_archived_messages_source_unique
			ON archived_messages (business_connection_id, source_chat_id, source_message_id);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_expires_at
			ON archived_messages (expires_at);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_deleted_at
			ON archived_messages (deleted_at);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_message_kind
			ON archived_messages (message_kind);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_source_username
			ON archived_messages (source_username COLLATE NOCASE, updated_at DESC, id DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_source_username_lc
			ON archived_messages (source_username_lc, updated_at DESC, id DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_archived_messages_media_group
			ON archived_messages (business_connection_id, source_chat_id, media_group_id);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_message_versions_parent_version_unique
			ON message_versions (parent_message_id, version_no);`,
		`CREATE INDEX IF NOT EXISTS idx_message_versions_parent
			ON message_versions (parent_message_id);`,
		`CREATE INDEX IF NOT EXISTS idx_business_targets_last_seen
			ON business_targets (last_seen_at DESC, id DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_archive_copies_parent
			ON archive_copies (parent_message_id);`,
		`CREATE INDEX IF NOT EXISTS idx_archive_copies_pending
			ON archive_copies (send_status, sent_at);`,
		`CREATE INDEX IF NOT EXISTS idx_pending_deletes_due
			ON pending_deletes (status, next_attempt_at);`,
		`CREATE INDEX IF NOT EXISTS idx_business_connections_enabled
			ON business_connections (is_enabled, updated_at DESC);`,
	}

	for _, query := range indexes {
		q := query
		if err := r.withRetry(ctx, func() error {
			if _, err := r.db.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("recreate sqlite index: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *SQLiteRepository) purgeSQLiteFreePages(ctx context.Context) error {
	for _, query := range []string{
		"PRAGMA wal_checkpoint(TRUNCATE);",
		"VACUUM;",
		"PRAGMA wal_checkpoint(TRUNCATE);",
	} {
		q := query
		if err := r.withRetry(ctx, func() error {
			if _, err := r.db.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("purge sqlite free pages: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *SQLiteRepository) withRetry(ctx context.Context, operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < retryAttempts; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		if !isSQLiteLockedError(err) {
			return err
		}

		lastErr = err
		waitFor := time.Duration(attempt+1) * retryWaitDuration
		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled while waiting for sqlite lock release: %w", ctx.Err())
		case <-time.After(waitFor):
		}
	}

	return fmt.Errorf("sqlite locked after retries: %w", lastErr)
}

func (r *SQLiteRepository) ensureArchivedMessagesColumn(ctx context.Context, columnName string, columnType string) error {
	return r.ensureTableColumn(ctx, "archived_messages", columnName, columnType)
}

func (r *SQLiteRepository) ensureTableColumn(ctx context.Context, tableName string, columnName string, columnType string) error {
	tableName = strings.TrimSpace(tableName)
	columnName = strings.TrimSpace(columnName)
	columnType = strings.TrimSpace(columnType)
	if tableName == "" || columnName == "" || columnType == "" {
		return fmt.Errorf("invalid column specification")
	}
	if !isSafeSQLiteIdentifier(tableName) || !isSafeSQLiteIdentifier(columnName) {
		return fmt.Errorf("unsafe sqlite identifier")
	}

	columns, err := r.tableColumns(ctx, tableName)
	if err != nil {
		return fmt.Errorf("check column %s.%s existence: %w", tableName, columnName, err)
	}

	if columns[strings.ToLower(columnName)] {
		return nil
	}

	query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", tableName, columnName, columnType)
	if err := r.withRetry(ctx, func() error {
		if _, err := r.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("add column %s.%s: %w", tableName, columnName, err)
		}
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (r *SQLiteRepository) tableColumns(ctx context.Context, tableName string) (map[string]bool, error) {
	tableName = strings.TrimSpace(tableName)
	if !isSafeSQLiteIdentifier(tableName) {
		return nil, fmt.Errorf("unsafe sqlite identifier")
	}

	columns := make(map[string]bool)
	if err := r.withRetry(ctx, func() error {
		rows, err := r.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s);", tableName))
		if err != nil {
			return fmt.Errorf("pragma table_info %s: %w", tableName, err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				cid       int
				name      string
				typeName  string
				notNull   int
				defaultV  sql.NullString
				primaryID int
			)
			if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultV, &primaryID); err != nil {
				return fmt.Errorf("scan table_info row: %w", err)
			}
			columns[strings.ToLower(name)] = true
		}

		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate table_info rows: %w", err)
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return columns, nil
}

func hasAnyColumn(columns map[string]bool, names ...string) bool {
	for _, name := range names {
		if columns[strings.ToLower(name)] {
			return true
		}
	}
	return false
}

func isSafeSQLiteIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func isSQLiteLockedError(err error) bool {
	if err == nil {
		return false
	}
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "database is locked") || strings.Contains(value, "database is busy")
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessageVersion(scanner rowScanner) (*MessageVersion, error) {
	var (
		version             MessageVersion
		archiveMessageIDRaw sql.NullInt64
		editDateRaw         sql.NullInt64
		createdAtRaw        string
	)

	if err := scanner.Scan(
		&version.ID,
		&version.ParentMessageID,
		&version.VersionNo,
		&version.ContentType,
		&archiveMessageIDRaw,
		&editDateRaw,
		&createdAtRaw,
	); err != nil {
		return nil, err
	}

	if archiveMessageIDRaw.Valid {
		archiveMessageID := int(archiveMessageIDRaw.Int64)
		version.ArchiveMessageID = &archiveMessageID
	}
	if editDateRaw.Valid {
		editDate := editDateRaw.Int64
		version.EditDate = &editDate
	}

	createdAt, err := parseDBTime(createdAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse version created_at: %w", err)
	}
	version.CreatedAt = createdAt

	return &version, nil
}

func scanArchiveCopy(scanner rowScanner) (*ArchiveCopy, error) {
	var (
		copy                     ArchiveCopy
		versionIDRaw             sql.NullInt64
		archiveMessageIDRaw      sql.NullInt64
		metadataMessageIDRaw     sql.NullInt64
		errorTextRaw             sql.NullString
		sentAtRaw                sql.NullString
		deletedFromArchiveAtRaw  sql.NullString
	)

	if err := scanner.Scan(
		&copy.ID,
		&copy.ParentMessageID,
		&versionIDRaw,
		&copy.VersionNo,
		&copy.ArchiveChatID,
		&archiveMessageIDRaw,
		&metadataMessageIDRaw,
		&copy.SendStatus,
		&errorTextRaw,
		&sentAtRaw,
		&deletedFromArchiveAtRaw,
	); err != nil {
		return nil, err
	}

	if versionIDRaw.Valid {
		copy.VersionID = versionIDRaw.Int64
	}
	if archiveMessageIDRaw.Valid {
		value := int(archiveMessageIDRaw.Int64)
		copy.ArchiveMessageID = &value
	}
	if metadataMessageIDRaw.Valid {
		value := int(metadataMessageIDRaw.Int64)
		copy.MetadataMessageID = &value
	}
	if errorTextRaw.Valid {
		copy.ErrorText = errorTextRaw.String
	}
	if sentAtRaw.Valid {
		parsed, err := parseDBTime(sentAtRaw.String)
		if err != nil {
			return nil, fmt.Errorf("parse archive copy sent_at: %w", err)
		}
		copy.SentAt = &parsed
	}
	if deletedFromArchiveAtRaw.Valid {
		parsed, err := parseDBTime(deletedFromArchiveAtRaw.String)
		if err != nil {
			return nil, fmt.Errorf("parse archive copy deleted_from_archive_at: %w", err)
		}
		copy.DeletedFromArchive = &parsed
	}

	return &copy, nil
}

func scanPendingDelete(scanner rowScanner) (*PendingDelete, error) {
	var (
		pending       PendingDelete
		firstSeenRaw  string
		nextRaw       string
		lastErrorRaw  sql.NullString
		updatedAtRaw  string
	)

	if err := scanner.Scan(
		&pending.ID,
		&pending.BusinessConnectionID,
		&pending.SourceChatID,
		&pending.SourceMessageID,
		&firstSeenRaw,
		&nextRaw,
		&pending.AttemptCount,
		&pending.Status,
		&lastErrorRaw,
		&updatedAtRaw,
	); err != nil {
		return nil, err
	}

	firstSeenAt, err := parseDBTime(firstSeenRaw)
	if err != nil {
		return nil, fmt.Errorf("parse pending delete first_seen_at: %w", err)
	}
	nextAttemptAt, err := parseDBTime(nextRaw)
	if err != nil {
		return nil, fmt.Errorf("parse pending delete next_attempt_at: %w", err)
	}
	updatedAt, err := parseDBTime(updatedAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse pending delete updated_at: %w", err)
	}

	pending.FirstSeenAt = firstSeenAt
	pending.NextAttemptAt = nextAttemptAt
	pending.UpdatedAt = updatedAt
	if lastErrorRaw.Valid {
		pending.LastError = lastErrorRaw.String
	}

	return &pending, nil
}

func scanArchivedMessage(scanner rowScanner) (*ArchivedMessage, error) {
	var (
		message             ArchivedMessage
		sourceFromID        sql.NullInt64
		sourceUsername      sql.NullString
		sourceDisplayName   sql.NullString
		mediaGroupID        sql.NullString
		archiveChatID       sql.NullInt64
		archiveMessageID    sql.NullInt64
		createdAtRaw        string
		updatedAtRaw        string
		expiresAtRaw        string
		deletedAtRaw        sql.NullString
		deletionNotifiedRaw sql.NullString
		resentToOwnerAtRaw  sql.NullString
	)

	if err := scanner.Scan(
		&message.ID,
		&message.BusinessConnectionID,
		&message.SourceChatID,
		&message.SourceMessageID,
		&sourceFromID,
		&sourceUsername,
		&sourceDisplayName,
		&archiveChatID,
		&archiveMessageID,
		&message.OwnerChatID,
		&message.MessageKind,
		&message.ContentType,
		&mediaGroupID,
		&createdAtRaw,
		&updatedAtRaw,
		&expiresAtRaw,
		&deletedAtRaw,
		&deletionNotifiedRaw,
		&resentToOwnerAtRaw,
	); err != nil {
		return nil, err
	}

	if sourceFromID.Valid {
		message.SourceFromID = &sourceFromID.Int64
	}
	if sourceUsername.Valid {
		message.SourceUsername = sourceUsername.String
	}
	if sourceDisplayName.Valid {
		message.SourceDisplayName = sourceDisplayName.String
	}
	if mediaGroupID.Valid {
		message.MediaGroupID = mediaGroupID.String
	}
	if archiveChatID.Valid {
		value := archiveChatID.Int64
		message.ArchiveChatID = &value
	}
	if archiveMessageID.Valid {
		value := int(archiveMessageID.Int64)
		message.ArchiveMessageID = &value
	}

	createdAt, err := parseDBTime(createdAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	updatedAt, err := parseDBTime(updatedAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	expiresAt, err := parseDBTime(expiresAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse expires_at: %w", err)
	}

	message.CreatedAt = createdAt
	message.UpdatedAt = updatedAt
	message.ExpiresAt = expiresAt

	if deletedAtRaw.Valid {
		parsed, err := parseDBTime(deletedAtRaw.String)
		if err != nil {
			return nil, fmt.Errorf("parse deleted_at: %w", err)
		}
		message.DeletedAt = &parsed
	}
	if deletionNotifiedRaw.Valid {
		parsed, err := parseDBTime(deletionNotifiedRaw.String)
		if err != nil {
			return nil, fmt.Errorf("parse deletion_notified_at: %w", err)
		}
		message.DeletionNotifiedAt = &parsed
	}
	if resentToOwnerAtRaw.Valid {
		parsed, err := parseDBTime(resentToOwnerAtRaw.String)
		if err != nil {
			return nil, fmt.Errorf("parse resent_to_owner_at: %w", err)
		}
		message.ResentToOwnerAt = &parsed
	}

	return &message, nil
}

func parseDBTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}

	layouts := []string{
		dbTimeLayout,
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
	}

	for _, layout := range layouts {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			return parsed.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported time format %q", raw)
}

func toDBTime(value time.Time) string {
	return value.UTC().Format(dbTimeLayout)
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return toDBTime(*value)
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func zeroNilInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}
