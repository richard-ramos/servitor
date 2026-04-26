package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestDBContextBinding(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "servitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	c := Context{ID: "ctx_test", Kind: ContextKindScratch, State: ContextStateActive, WorkspaceDir: "/tmp/workspace"}
	if err := CreateContext(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	if err := BindTopic(ctx, db, 10, 20, c.ID); err != nil {
		t.Fatal(err)
	}
	got, err := GetBoundContext(ctx, db, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID {
		t.Fatalf("got %q want %q", got.ID, c.ID)
	}
	if got.ReasoningEffort != defaultReasoningEffort {
		t.Fatalf("got reasoning effort %q want %q", got.ReasoningEffort, defaultReasoningEffort)
	}
	if got.DisplayName != "" {
		t.Fatalf("got display name %q want empty", got.DisplayName)
	}
	if err := DetachTopic(ctx, db, 10, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := GetBoundContext(ctx, db, 10, 20); !isNoRows(err) {
		t.Fatalf("expected no rows, got %v", err)
	}
}

func TestContextDisplayNameResolution(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "servitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	c := Context{ID: "ctx_test", Kind: ContextKindScratch, State: ContextStateActive, WorkspaceDir: "/tmp/workspace"}
	if err := CreateContext(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	if err := UpdateContextDisplayName(ctx, db, c.ID, "work"); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveContext(ctx, db, "work")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID || got.DisplayName != "work" {
		t.Fatalf("resolved wrong context: %+v", got)
	}
	list, err := ListContexts(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].DisplayName != "work" {
		t.Fatalf("unexpected contexts: %+v", list)
	}
}

func TestClaimNextPendingIsOnePerContext(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "servitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	c := Context{ID: "ctx_test", Kind: ContextKindScratch, State: ContextStateActive, WorkspaceDir: "/tmp/workspace"}
	if err := CreateContext(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	firstID, err := Enqueue(ctx, db, c.ID, 0, 0, "first", false)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := Enqueue(ctx, db, c.ID, 0, 0, "second", false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ClaimNextPending(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != firstID || first.Status != "running" {
		t.Fatalf("got claimed item %+v, want id %d running", first, firstID)
	}
	if _, err := ClaimNextPending(ctx, db); !isNoRows(err) {
		t.Fatalf("expected no claim while context has running item, got %v", err)
	}
	if err := MarkQueueDone(ctx, db, firstID); err != nil {
		t.Fatal(err)
	}
	second, err := ClaimNextPending(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != secondID || second.Status != "running" {
		t.Fatalf("got claimed item %+v, want id %d running", second, secondID)
	}
}

func TestCancelAndRetryQueueItem(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "servitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	c := Context{ID: "ctx_test", Kind: ContextKindScratch, State: ContextStateActive, WorkspaceDir: "/tmp/workspace"}
	if err := CreateContext(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	queueID, err := Enqueue(ctx, db, c.ID, 0, 0, "try this", false)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := CancelPendingQueueForContext(ctx, db, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != 1 {
		t.Fatalf("cancelled %d items, want 1", cancelled)
	}
	failed, err := LatestFailedQueueForContext(ctx, db, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.ID != queueID || failed.FailureClass != "cancelled" {
		t.Fatalf("unexpected failed queue item: %+v", failed)
	}
	retryID, err := RetryQueueItem(ctx, db, failed)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := GetQueueItemForContext(ctx, db, c.ID, retryID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != QueueStatusPending || retry.Prompt != "try this" {
		t.Fatalf("unexpected retry queue item: %+v", retry)
	}
}

func TestMigrateV1ToV2PreservesCronSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servitor.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE contexts (id TEXT PRIMARY KEY, kind TEXT NOT NULL, state TEXT NOT NULL, repo_url TEXT NOT NULL DEFAULT '', workspace_dir TEXT NOT NULL, codex_session TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`CREATE TABLE queue (id INTEGER PRIMARY KEY AUTOINCREMENT, context_id TEXT NOT NULL, message_id INTEGER, telegram_msg_id INTEGER NOT NULL DEFAULT 0, prompt TEXT NOT NULL, resume INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0, failure_class TEXT NOT NULL DEFAULT '', current_run_id INTEGER NOT NULL DEFAULT 0, next_retry_at TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')), completed_at TEXT)`,
		`CREATE TABLE schedules (id TEXT PRIMARY KEY, context_id TEXT NOT NULL, cron_expr TEXT NOT NULL, prompt TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, last_run_at TEXT, next_run_at TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`INSERT INTO contexts(id, kind, state, workspace_dir) VALUES ('ctx_test', 'scratch', 'active', '/tmp/workspace')`,
		`INSERT INTO schedules(id, context_id, cron_expr, prompt, enabled, next_run_at) VALUES ('sch_test', 'ctx_test', '*/5 * * * *', 'hello', 1, '2026-04-25 10:00:00')`,
		`PRAGMA user_version = 1`,
	}
	for _, stmt := range stmts {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != dbSchemaVersion {
		t.Fatalf("got version %d want %d", version, dbSchemaVersion)
	}
	got, err := GetContextByID(context.Background(), db, "ctx_test")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReasoningEffort != defaultReasoningEffort {
		t.Fatalf("got reasoning effort %q want %q", got.ReasoningEffort, defaultReasoningEffort)
	}
	if got.DisplayName != "" {
		t.Fatalf("got display name %q want empty", got.DisplayName)
	}
	s, err := GetSchedule(context.Background(), db, "sch_test", "ctx_test")
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != ScheduleKindCron || s.Status != ScheduleStatusActive || s.CronExpr != "*/5 * * * *" {
		t.Fatalf("bad migrated schedule: %+v", s)
	}
}
