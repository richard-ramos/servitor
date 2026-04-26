package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProcessQueueItemMarksEarlyErrorFailed(t *testing.T) {
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
	qid, err := Enqueue(ctx, db, c.ID, 0, 0, "scheduled without binding", false)
	if err != nil {
		t.Fatal(err)
	}
	item, err := ClaimNextPending(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, cfg: Config{}, redactor: NewRedactor()}
	if err := app.processQueueItem(ctx, item); err == nil {
		t.Fatal("expected missing binding error")
	}
	var status, failure string
	if err := db.QueryRow(`SELECT status, failure_class FROM queue WHERE id=?`, qid).Scan(&status, &failure); err != nil {
		t.Fatal(err)
	}
	if status != QueueStatusFailed || failure != "host_error" {
		t.Fatalf("got status=%s failure=%s", status, failure)
	}
}
