package main

import (
	"context"
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
	if err := DetachTopic(ctx, db, 10, 20); err != nil {
		t.Fatal(err)
	}
	if _, err := GetBoundContext(ctx, db, 10, 20); !isNoRows(err) {
		t.Fatalf("expected no rows, got %v", err)
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
