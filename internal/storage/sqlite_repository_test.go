package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTempRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	dir := t.TempDir()
	repo, err := NewSQLiteRepository(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestMigrate_CreatesSchemaAndRecordsVersion(t *testing.T) {
	repo := newTempRepo(t)

	rows, err := repo.db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name;`)
	if err != nil {
		t.Fatalf("query schema: %v", err)
	}
	defer rows.Close()

	expected := map[string]bool{
		"archive_copies":       false,
		"archived_messages":    false,
		"business_connections": false,
		"business_targets":     false,
		"message_versions":     false,
		"pending_deletes":      false,
		"schema_migrations":    false,
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := expected[name]; ok {
			expected[name] = true
		}
	}
	for table, found := range expected {
		if !found {
			t.Errorf("expected table %q to exist after migration", table)
		}
	}

	var version int
	if err := repo.db.QueryRow(`SELECT MAX(version) FROM schema_migrations;`).Scan(&version); err != nil {
		t.Fatalf("schema_migrations not populated: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("expected schema version %d, got %d", currentSchemaVersion, version)
	}
}

func TestMigrate_NoLegacyContentColumnsInTargetSchema(t *testing.T) {
	repo := newTempRepo(t)

	forbidden := map[string][]string{
		"archived_messages": {"file_id", "file_unique_id", "text_preview", "caption"},
		"message_versions":  {"text_full", "text_preview", "caption_full", "caption", "file_id", "file_unique_id", "metadata_json"},
	}
	for table, cols := range forbidden {
		actual, err := repo.tableColumns(context.Background(), table)
		if err != nil {
			t.Fatalf("read columns of %s: %v", table, err)
		}
		for _, col := range cols {
			if actual[col] {
				t.Errorf("table %s must not contain forbidden content column %q", table, col)
			}
		}
	}
}

func TestInsertNextVersion_Atomic(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	parent := &ArchivedMessage{
		BusinessConnectionID: "bc-atomic",
		SourceChatID:         100,
		SourceMessageID:      1,
		OwnerChatID:          12345,
		MessageKind:          "text",
		ContentType:          "text",
		CreatedAt:            now,
		UpdatedAt:            now,
		ExpiresAt:            now.Add(time.Hour),
	}
	if _, err := repo.InsertIfNotExists(ctx, parent); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	parentLoaded, err := repo.GetBySource(ctx, "bc-atomic", 100, 1)
	if err != nil || parentLoaded == nil {
		t.Fatalf("get parent: %v %v", err, parentLoaded)
	}

	// Three sequential next-version inserts must produce 1, 2, 3.
	for i := 1; i <= 3; i++ {
		_, no, err := repo.InsertNextVersion(ctx, &MessageVersion{
			ParentMessageID: parentLoaded.ID,
			ContentType:     "text",
			CreatedAt:       now.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("insert next version %d: %v", i, err)
		}
		if no != i {
			t.Fatalf("expected version_no %d, got %d", i, no)
		}
	}
}

func TestBusinessConnection_UpsertGetList(t *testing.T) {
	repo := newTempRepo(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	connA := &BusinessConnection{
		ID:               "bc-a",
		OwnerUserID:      111,
		OwnerUserChatID:  111,
		OwnerUsername:    "alice",
		OwnerDisplayName: "Alice",
		IsEnabled:        true,
		CanReply:         true,
		ConnectedAt:      now,
		UpdatedAt:        now,
	}
	if err := repo.UpsertBusinessConnection(ctx, connA); err != nil {
		t.Fatalf("upsert connA: %v", err)
	}

	loaded, err := repo.GetBusinessConnection(ctx, "bc-a")
	if err != nil || loaded == nil {
		t.Fatalf("get connA: %v %v", err, loaded)
	}
	if !loaded.IsEnabled || !loaded.CanReply || loaded.OwnerUsername != "alice" {
		t.Fatalf("connA round-trip mismatch: %+v", loaded)
	}

	// upsert as disabled — must update without losing connected_at
	disconnectedAt := now.Add(time.Minute)
	connADisabled := *connA
	connADisabled.IsEnabled = false
	connADisabled.CanReply = false
	connADisabled.UpdatedAt = disconnectedAt
	connADisabled.DisconnectedAt = &disconnectedAt
	if err := repo.UpsertBusinessConnection(ctx, &connADisabled); err != nil {
		t.Fatalf("upsert disable: %v", err)
	}
	loaded, _ = repo.GetBusinessConnection(ctx, "bc-a")
	if loaded.IsEnabled {
		t.Fatalf("expected disabled after second upsert")
	}
	if loaded.DisconnectedAt == nil {
		t.Fatalf("expected disconnected_at to be persisted")
	}
	if !loaded.ConnectedAt.Equal(now) {
		t.Fatalf("expected connected_at preserved, got %v want %v", loaded.ConnectedAt, now)
	}

	// add another, enabled
	if err := repo.UpsertBusinessConnection(ctx, &BusinessConnection{
		ID:              "bc-b",
		OwnerUserID:     222,
		OwnerUserChatID: 222,
		OwnerUsername:   "bob",
		IsEnabled:       true,
		CanReply:        false,
		ConnectedAt:     now,
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("upsert connB: %v", err)
	}

	all, err := repo.ListBusinessConnections(ctx, false)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 total connections, got %d", len(all))
	}

	enabled, err := repo.ListBusinessConnections(ctx, true)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if len(enabled) != 1 || enabled[0].ID != "bc-b" {
		t.Fatalf("expected only bc-b in enabled list, got %+v", enabled)
	}
}

func TestPendingDelete_SurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pending.db")

	repo, err := NewSQLiteRepository(dbPath)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	if err := repo.UpsertPendingDelete(context.Background(), &PendingDelete{
		BusinessConnectionID: "bc-survive",
		SourceChatID:         5,
		SourceMessageID:      77,
		FirstSeenAt:          now,
		NextAttemptAt:        now,
		Status:               "pending",
		LastError:            "metadata_not_found",
		UpdatedAt:            now,
	}); err != nil {
		t.Fatalf("upsert pending: %v", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	repo2, err := NewSQLiteRepository(dbPath)
	if err != nil {
		t.Fatalf("reopen repo: %v", err)
	}
	defer repo2.Close()
	if err := repo2.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate again: %v", err)
	}

	due, err := repo2.ListDuePendingDeletes(context.Background(), now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 pending delete after reopen, got %d", len(due))
	}
	if due[0].SourceMessageID != 77 {
		t.Fatalf("expected pending source_message_id=77, got %d", due[0].SourceMessageID)
	}
}
