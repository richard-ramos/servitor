package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareContextCodexAssetsRejectsSkillSymlink(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DataDir: filepath.Join(root, "data"), SkillsDir: filepath.Join(root, "skills")}
	ctx := context.Background()
	db, err := OpenDB(filepath.Join(root, "servitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := Context{ID: "ctx_test", Kind: ContextKindScratch, State: ContextStateActive, WorkspaceDir: ContextWorkspaceDir(cfg, "ctx_test")}
	if err := os.MkdirAll(ContextCodexDir(cfg, c.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CreateContext(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(cfg.SkillsDir, "bad")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.md"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.md"), filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := SetContextSkill(ctx, db, c.ID, "bad", true); err != nil {
		t.Fatal(err)
	}
	if err := PrepareContextCodexAssets(ctx, db, cfg, c); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
