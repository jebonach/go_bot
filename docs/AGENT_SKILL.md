# Agent Skill: Removed Messages Bot Code Map

Use this file as the canonical project map for future agent work. It describes what each package does, which functions own each workflow, and which invariants must not be broken.

## Non-Negotiable Architecture

SQLite is not a content store.

Do not store message text, captions, media metadata JSON, raw Telegram updates, or local media bytes in SQLite.

Archive chat is the content store. SQLite is only a lightweight index and workflow state store.

Allowed SQLite data:

- source message identity;
- archive message identity;
- version numbers;
- content type labels;
- `media_group_id` (for albums — does not contain message content);
- timestamps;
- retention expiration;
- delete/edit processing state;
- archive_copies outbox state (pending/sent/failed) and `error_text`;
- business target mappings for `/send`;
- minimal sender identity needed for notifications and target lookup.

Disallowed SQLite data:

- full message text;
- text preview;
- caption;
- caption preview;
- file id;
- file unique id;
- media metadata JSON;
- raw Telegram update JSON;
- media file bytes;
- bot token.

If a future change needs message content, put it in archive chat, not in SQLite.

## Runtime Flow

### Startup

Entry point:

- `cmd/main.go`

Startup chain:

1. `config.LoadConfig()`
2. `logging.New(cfg.LogLevel)`
3. `bot.Init(cfg, logger)`

### Bot Initialization

File:

- `internal/bot/bot.go`

Main function:

- `Init(cfg *config.Config, logger *logging.Logger) error`

Responsibilities:

- create cancellable context with `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` (graceful shutdown for both Ctrl-C and container SIGTERM);
- open SQLite repository;
- run migrations;
- create Telegram bot client;
- set allowed updates;
- route all updates to `BusinessMessageService`;
- start cleanup loop;
- start pending delete loop;
- start media-group flush loop;
- block on `b.Start(ctx)`.

Allowed updates:

- regular `message`;
- `business_connection`;
- `business_message`;
- `edited_business_message`;
- `deleted_business_messages`.

Important routing rules:

- `business_connection` goes to `HandleBusinessConnection`;
- `business_message` goes to `HandleBusinessMessage`;
- `edited_business_message` goes to `HandleEditedBusinessMessage`;
- `deleted_business_messages` goes to `HandleDeletedBusinessMessages`;
- regular owner messages may be handled by `HandleOwnerCommand` (commands `/send` and `/connections`);
- `/start` and `/id` reply to the **sender's** chat (`update.Message.Chat.ID`), not to a static config chat id.

## Configuration

File:

- `internal/config/config.go`

Main function:

- `LoadConfig() (*Config, error)`

Required environment:

- `BOT_TOKEN`;
- `ARCHIVE_CHAT_ID`;
- `OWNER_CHAT_ID`.

Important optional environment:

- `RETENTION_AUDIO_HOURS`;
- `RETENTION_PHOTO_HOURS`;
- `RETENTION_TEXT_HOURS`;
- `RETENTION_OTHER_MEDIA_HOURS`;
- `CLEANUP_INTERVAL_MINUTES`;
- `DELETE_EXPIRED_FROM_ARCHIVE`;
- `NOTIFY_ON_DELETE`;
- `RESEND_ARCHIVED_COPY_ON_DELETE`;
- `SQLITE_PATH`;
- `LOG_LEVEL`.

Do not reintroduce `STORE_FULL_TEXT` or `STORE_FULL_CAPTION`. The current architecture intentionally avoids SQL content storage.

## Service Layer

Primary file:

- `internal/service/business_service.go`

Primary type:

- `BusinessMessageService`

Dependencies:

- `config.Config`;
- `storage.Repository`;
- `TelegramClient`;
- `logging.Logger`.

### TelegramClient Interface (segregated)

Defined in:

- `internal/service/business_service.go`

`TelegramClient` is composed from four narrower interfaces (interface segregation):

- `ArchiveSender` — `CopyMessage`, `DeleteMessage`, `SendMessage`, `SendPhotoByFileID`, `SendVoiceByFileID`, `SendAudioByFileID`, `SendDocumentByFileID`, `SendVideoByFileID`, `SendAnimationByFileID`, `SendStickerByFileID`, `SendVideoNoteByFileID`;
- `OwnerNotifier` — `SendOwnerNotification`;
- `BusinessSender` — `SendBusinessMessage`;
- `ErrorClassifier` — `IsMissingMessageError`.

Implemented by:

- `internal/telegram/client.go` (`Client`).

The `Client` wraps `github.com/go-telegram/bot`. Every API call goes through `callWithRetry`, which:

- honors `retry after N` from Telegram 429 responses;
- exponentially backs off transient/network/server errors (`baseDelay * 2^attempt`, capped at 30s);
- never retries `Forbidden`, `Unauthorized`, `BadRequest`, or missing-message errors;
- bounds retries to 4 attempts.

## New Business Message Workflow

Function:

- `HandleBusinessMessage(ctx context.Context, message *models.Message)`

File:

- `internal/service/business_service.go`

Steps:

1. Ignore nil message.
2. Require `BusinessConnectionID`.
3. Classify message via `ClassifyMessage`.
4. Build an `ArchivedMessage` record with source ids, sender identity, content type, retention timestamps, and `media_group_id`.
5. Update `business_targets` via `upsertBusinessTarget`.
6. Insert message identity using `repo.InsertIfNotExists`.
7. Load parent row with `repo.GetBySource`.
8. Reconcile matching pending delete if one exists.
9. Call `archiveAndPersistVersion(parent, message, classification, explicitVersionNo=1, atomicNextVersion=false)`:
    1. Insert version row (`InsertVersion`) with `archive_message_id=NULL` and `edit_date` from message.
    2. Build archive action with `buildArchiveActionWithVersion`.
    3. Insert pending `archive_copies` row (status='pending').
    4. Send to archive chat via `sendArchiveAction`.
    5. On success: `UpdateArchiveCopyOnSend`, `UpdateVersionArchiveMessageID`, `SetArchiveCopy`.
    6. On failure: `UpdateArchiveCopyOnFailure`.
10. Track album in in-memory `mediaGroupBuffer` if `MediaGroupID` is set.

Critical invariants:

- Do not put text/caption into `ArchivedMessage` or `MessageVersion`.
- Archive chat must receive the actual content.
- SQLite stores ids and status only.
- Pending archive_copies row must be written BEFORE the Telegram send. Crashes mid-flight leave a detectable `pending` row.

## Edit Workflow

Function:

- `HandleEditedBusinessMessage(ctx context.Context, message *models.Message)`

File:

- `internal/service/business_service.go`

Steps:

1. Ignore nil message.
2. Require `BusinessConnectionID`.
3. Load existing source row with `repo.GetBySource`.
4. Load latest version with `ensureLatestVersion`.
5. **Edit idempotency**: if `oldVersion.EditDate == message.EditDate`, return early. Telegram occasionally re-delivers the same edit; we must not create duplicate versions.
6. Classify new message state.
7. Call `archiveAndPersistVersion(... atomicNextVersion=true)`:
    1. `InsertNextVersion` atomically computes `version_no = COALESCE(MAX(version_no), 0) + 1` and inserts via `INSERT ... SELECT ... RETURNING id, version_no`. No race window.
    2. Outbox: pending `archive_copies` → send → sent/failed.
8. `UpdateCurrentFromVersion` to update parent row (only if archive send succeeded; otherwise parent row stays pointing at previous archive copy).
9. Notify owner via `sendEditNotification`.
10. Copy old archive version to owner if old archive id exists.

Owner receives:

- an edit notification;
- the previous archived version copied from archive chat.

Critical invariants:

- Edit notification must not depend on text stored in SQLite.
- Previous content comes from archive chat via `CopyMessage`.
- `version_no` is computed by SQL, never by Go (no compare-and-swap race).
- Re-delivered edits with the same `edit_date` are silently ignored.

## Delete Workflow

Functions:

- `HandleDeletedBusinessMessages`
- `handleDeletedMessageID`
- `processDelete`
- `buildDeleteNotification`

File:

- `internal/service/business_service.go`

Steps:

1. Receive delete update.
2. For each source message id, load SQLite mapping.
3. If mapping is missing, store pending delete in SQLite.
4. If mapping exists, mark row deleted.
5. Send owner notification (notification text now also includes `media_group_id` if present).
6. Copy archive message from archive chat to owner if archive id exists.
7. If the deleted message has `media_group_id`, fetch siblings via `repo.ListByMediaGroup` and copy their archive copies to owner too.
8. Record notification/resend timestamps.

Critical invariants:

- Delete notification must not include SQL-stored message text.
- Deleted content is restored only by copying archive message.
- Album-aware delete: when one item is deleted, owner sees the entire album reconstructed from archive copies.

## Pending Delete Workflow

File:

- `internal/service/pending_delete.go`

Functions:

- `RunPendingDeleteLoop`;
- `reconcilePendingDeleteIfExists`;
- `reconcilePendingDeletes`;
- `enqueuePendingDelete`;
- `removePendingDelete`;
- `rescheduleStoredPendingDelete`.

Behavior:

- pending deletes are stored in SQLite table `pending_deletes`;
- retry loop polls due rows;
- rows are removed after successful processing;
- stale rows are removed after max age.

Critical invariant:

- Do not use an in-memory-only map for pending deletes. It must survive process restart.

## Cleanup Workflow

File:

- `internal/service/cleanup.go`

Functions:

- `RunCleanupLoop`;
- `runCleanupOnce`;
- `cleanupRecord`;
- `cleanupArchiveCopies`;
- `cleanupLegacyArchiveCopy`;
- `deleteArchiveMessageIfPresent`.

Behavior:

1. List expired rows with `repo.ListExpired`.
2. For each expired source message, list all archive copies.
3. Delete all archive messages and metadata messages from archive chat. Each delete is followed by a `cleanupDeleteBackoff` sleep (~35ms) to keep below Telegram 30 msg/s limits.
4. Mark archive copy deleted if it exists in `archive_copies`.
5. Delete source row from SQLite.

Critical invariant:

- Cleanup must delete every archived version, not just the latest `archive_message_id`.

## Media Group Workflow

File:

- `internal/service/media_group.go`

Functions:

- `trackMediaGroup` — buffers album items by `media_group_id` for grouping awareness;
- `RunMediaGroupFlushLoop` — periodically removes stale album buckets;
- `flushStaleMediaGroups` — emits a debug log line and prunes the buffer.

Behavior:

- Each album item is archived independently (per-message) — same as before. The buffer is purely informational.
- The album-aware delete reconstruction lives in `processDelete` (see Delete Workflow), backed by `repo.ListByMediaGroup`.

Critical invariant:

- Per-item archive sends remain synchronous. The buffer must not block the message handler.

## Business Connection Workflow

File:

- `internal/service/business_connection.go`

Function:

- `HandleBusinessConnection(ctx context.Context, conn *models.BusinessConnection)`

Steps:

1. Reject empty `conn.ID`.
2. Load previous record via `repo.GetBusinessConnection`.
3. Build a fresh `storage.BusinessConnection` (preserving original `connected_at` if a previous record exists).
4. `repo.UpsertBusinessConnection`.
5. If `is_enabled` or `can_reply` changed (or no previous record existed), send the owner a notification: `established`, `re-enabled`, `disabled`, or `rights changed`.

Helper used by the message path:

- `isBusinessConnectionActive(ctx, id) bool` — returns true when no record exists yet (race) or when the record is enabled. Logs a warning when an incoming message arrives on a disabled connection but does NOT drop the message (fail-open archiving).

Critical invariants:

- Bot must request `AllowedUpdateBusinessConnection` from Telegram, otherwise updates never arrive and the bot has no way to know it was disconnected.
- `connected_at` must be preserved across upserts (do not overwrite with the latest `Date` from a re-enable event).
- Disabled connections stay in the table — never delete rows on disconnect; the record + `disconnected_at` is the audit trail.

## `/send` Command Workflow

File:

- `internal/service/send_command.go`

Entry:

- `HandleOwnerCommand`

Command format:

```text
/send [@username, @username] [message text]
```

Key functions:

- `isOwnerAuthorized`;
- `parseSendCommand`;
- `parseRecipients`;
- `executeSendCommand`;
- `buildSendSummary`.

Behavior:

1. Only owner may use the command, **and only from the private chat with the bot**. Both `Chat.ID == OwnerChatID` AND `From.ID == OwnerChatID` are required. This blocks `/send` from groups where `From.ID` happens to match the owner.
2. Recipients are normalized to lowercase username keys.
3. Repository looks up targets via `FindLatestChatTargetByUsername` (lowercased input is used against the indexed `source_username_lc` column — no `TRIM`/`LOWER` in WHERE).
4. Repository validates the target's `business_connection_id` via `GetBusinessConnection` when a registry row exists.
5. Send is performed through `SendBusinessMessage` when the connection is unknown, or when it is known with `is_enabled=true` and `can_reply=true`.
6. Owner receives success/unknown/failed summary.

Critical invariant:

- `/send` requires `business_targets`.
- `/send` must block known disabled or `can_reply=false` connections.
- `/send` must not block an unknown `business_connections` row if `business_targets` has a stored `business_connection_id`; this handles already-linked Business accounts where Telegram has not delivered a `business_connection` update yet.
- Do not make `/send` depend on message retention rows.
- Do not relax the private-chat check.

## Message Classification

File:

- `internal/service/classifier.go`

Function:

- `ClassifyMessage(message *models.Message) Classification`

Supported content types:

- native archive copy: `photo`, `voice`, `audio`, `document`, `video`, `animation`, `sticker`, `video_note`;
- text archive copy: `text`;
- metadata archive copy: `contact`, `location`, `venue`, `poll`, `dice`, `paid_media`, `story`, `checklist`, `game`, invoice/payment/refund, gifts, giveaways, forum/service events, shared users/chats, web app data, voice chat events;
- safe metadata fallback: `unknown`.

Classification is in-memory only. It is used to choose archive send method and retention bucket. Do not persist message text, caption, file ids, or metadata JSON from this type.

## Archive Action Builder

File:

- `internal/service/archive_action.go`

Functions:

- `buildArchiveAction`;
- `buildArchiveActionWithVersion`;
- `buildArchiveHeader`;
- `joinWithBody`;
- `clampText`.

Behavior:

- builds message sent to archive chat;
- prepends archive header with source ids and version;
- sends text/caption to archive chat only;
- sends metadata-only representations for Telegram types that cannot be copied natively;
- respects Telegram message and caption limits.

Critical invariant:

- This file may touch message content because it sends content to archive chat.
- It must not write content to SQLite.
- Do not add `business_connection_id` to archive message text; SQLite already has it for internal routing.

## Retention

File:

- `internal/service/retention.go`

Function:

- `calculateExpiresAt(now time.Time, cfg *config.Config, contentType string) time.Time`

Behavior:

- audio/voice use audio retention;
- photo uses photo retention;
- text uses text retention;
- other media and unknown use other-media retention.

## Source Identity

File:

- `internal/service/source_identity.go`

Functions:

- `normalizeUsernameForStorage`;
- `normalizeUsernameKey`;
- `formatUsernameOrPlaceholder`;
- `extractSourceUsernameFromMessage`;
- `extractSourceDisplayNameFromMessage`;
- `buildDisplayNameFromUser`;
- `buildDisplayNameFromChat`;
- `compactSingleLine`.

Behavior:

- extracts and normalizes sender identity;
- used for notifications and business target mapping.

This is allowed in SQLite because it is routing/identity metadata, not message content.

## Storage Layer

Files:

- `internal/storage/repository.go`;
- `internal/storage/sqlite_repository.go`.

Interface:

- `Repository`

Main entities:

- `ArchivedMessage` (now includes `MediaGroupID`);
- `MessageVersion` (now includes `EditDate`);
- `ArchiveCopy`;
- `BusinessSendTarget`;
- `BusinessConnection`;
- `PendingDelete`.

Important repository methods:

- `InsertIfNotExists`;
- `SetArchiveCopy`;
- `GetBySource`;
- `UpsertBusinessTarget`;
- `FindLatestChatTargetByUsername`;
- `UpdateCurrentFromVersion`;
- `InsertVersion` (used for v=1 on new messages and for bootstrap);
- `InsertNextVersion` (atomic `MAX+1` for edits);
- `UpdateVersionArchiveMessageID` (fills archive_message_id after successful send);
- `GetLatestVersionByParentID`;
- `InsertArchiveCopy` (writes pending row before Telegram send);
- `UpdateArchiveCopyOnSend` (transitions pending → sent);
- `UpdateArchiveCopyOnFailure` (transitions pending → failed);
- `ListPendingArchiveCopiesOlderThan` (recovery hook for orphan detection);
- `ListArchiveCopiesByMessageID`;
- `ListByMediaGroup` (album reconstruction on delete);
- `UpsertBusinessConnection` / `GetBusinessConnection` / `ListBusinessConnections` (connection registry);
- `MarkArchiveCopyDeleted`;
- `MarkDeletedIfUnset`;
- `RecordDeleteProcessing`;
- `UpsertPendingDelete`;
- `ListDuePendingDeletes`;
- `DeletePendingDelete`;
- `ListExpired`;
- `DeleteByID`.

SQLite tables:

- `archived_messages` (with `media_group_id`, `source_username_lc`);
- `message_versions` (with `edit_date`);
- `business_targets`;
- `business_connections` (one row per `business_connection_id`; never deleted on disconnect);
- `archive_copies` (acts as outbox for archive sends);
- `pending_deletes`;
- `schema_migrations` (`currentSchemaVersion = 3`).

Indexing notes:

- `idx_archived_messages_source_username_lc` on `(source_username_lc, updated_at DESC, id DESC)` — used by `findLatestArchivedChatTargetByUsername`. **Never** use `LOWER(...)` or `TRIM(...)` on indexed columns inside WHERE — store the lower-cased copy.
- `idx_archive_copies_pending` on `(send_status, sent_at)` — used by orphan recovery scan.
- `idx_archived_messages_media_group` on `(business_connection_id, source_chat_id, media_group_id)` — used by `ListByMediaGroup`.

Legacy migration:

- `removeLegacyColumns` detects old local test columns and rebuilds affected tables without them.
- Legacy content columns are not part of the target schema.
- After rebuild, `purgeSQLiteFreePages` runs WAL checkpoint and `VACUUM`.
- Do not reintroduce `text_preview`, `caption`, `text_full`, `caption_full`, `file_id`, `file_unique_id`, or `metadata_json` into SQLite tables.

## Telegram Client

File:

- `internal/telegram/client.go`

Type:

- `Client`

Responsibilities:

- thin wrapper over `github.com/go-telegram/bot`;
- returns message ids after send/copy;
- normalizes Telegram API errors for missing messages;
- uniformly retries via `callWithRetry` (see Service Layer above).

Methods:

- `CopyMessage`;
- `DeleteMessage`;
- `SendMessage`;
- `SendBusinessMessage`;
- `SendPhotoByFileID`;
- `SendVoiceByFileID`;
- `SendAudioByFileID`;
- `SendDocumentByFileID`;
- `SendVideoByFileID`;
- `SendAnimationByFileID`;
- `SendStickerByFileID`;
- `SendVideoNoteByFileID`;
- `SendOwnerNotification`;
- `IsMissingMessageError`.

Helpers:

- `callWithRetry` — single retry/backoff envelope;
- `parseRetryAfter` — parses `retry after N` from Telegram error text;
- `isRetryable` — distinguishes permanent client errors from transient ones.

## Logging

File:

- `internal/logging/logger.go`

Type:

- `Logger`

Methods:

- `Debugf`;
- `Infof`;
- `Warnf`;
- `Errorf`.

Use structured-ish key/value text in log messages. Include source chat id, source message id, content type, version number, archive message id, and `media_group_id` when relevant.

## Tests

Files:

- `internal/service/business_service_test.go`;
- `internal/service/business_service_outbox_test.go`;
- `internal/service/business_connection_test.go`;
- `internal/service/send_command_test.go`;
- `internal/storage/sqlite_repository_test.go`;
- `internal/telegram/client_test.go`.

Coverage now includes:

- archive action photo file id selection;
- version creation on new business message;
- atomic next version creation on edit (`InsertNextVersion`);
- duplicate edit_date is ignored (edit idempotency);
- copying the previous archived version to owner on edit;
- username/target handling;
- `/send` parsing and execution;
- `/send` rejected from group chat even if `From.ID` matches owner;
- delete notification identity;
- delete reconstructs full album from sibling `media_group_id` rows;
- cleanup all archive copies;
- outbox pending row written before Telegram send;
- outbox row marked failed on Telegram send error;
- SQLite migration on empty DB (creates expected tables, records schema version, no legacy content columns);
- `pending_deletes` survives close/reopen of the SQLite file;
- `parseRetryAfter` and `isRetryable` for the Telegram retry envelope;
- `business_connection` upsert on first delivery (notifies owner with "established");
- disable→record + owner notification + `disconnected_at` populated;
- no notification when state did not change;
- `/connections` lists known connections; non-owner is rejected;
- SQLite round-trip for `business_connections` (upsert/get/list with `onlyEnabled` filter).

## Current Environment Constraint

At the time of this document, `go` is not available in `PATH` on the local machine. `go test ./...` cannot run until Go is installed or added to `PATH`.

Do not claim compile/test success unless `go test ./...` actually ran.

## Safe Change Checklist

Before editing:

1. Read this file.
2. Check `git status --short`.
3. Do not reintroduce SQL content storage.
4. Preserve archive chat as the content source.
5. Preserve `/send` owner authorization (private chat only, both `Chat.ID` and `From.ID` must equal `OwnerChatID`).
6. Preserve cleanup of all archive copies.
7. Preserve outbox: a pending `archive_copies` row must exist before any Telegram archive send.
8. Preserve atomic version_no on edits — never compute `MAX+1` in Go.
9. Keep `AllowedUpdateBusinessConnection` in the allowed updates list. Without it the bot has no way to learn about disconnects.
10. Never delete rows from `business_connections` on disconnect — store `is_enabled=false` and `disconnected_at`.

After editing:

1. Run `git diff --check`.
2. Run `go test ./...` if Go is available.
3. Update `docs/BOT_OVERVIEW.md` if behavior changed.
4. Update this file if ownership or function responsibility changed.
