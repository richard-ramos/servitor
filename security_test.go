package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactor(t *testing.T) {
	r := NewRedactor("sk-testsecret123456789")
	got := r.Redact("token=sk-testsecret123456789 Authorization: Bearer abcdefghijklmnopqrstuvwxyz")
	if strings.Contains(got, "sk-testsecret") || strings.Contains(got, "abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secret leaked: %s", got)
	}
}

func TestValidateContextPathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DataDir: root}
	ctxDir := ContextDir(cfg, "ctx")
	if err := os.MkdirAll(ctxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateContextPath(cfg, "ctx", filepath.Join(ctxDir, "..")); err == nil {
		t.Fatal("expected escape rejection")
	}
}

func TestValidateContextPathRejectsDockerMountComma(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DataDir: root}
	ctxDir := ContextDir(cfg, "ctx")
	if err := os.MkdirAll(ctxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateContextPath(cfg, "ctx", filepath.Join(ctxDir, "bad,readonly")); err == nil {
		t.Fatal("expected comma rejection")
	}
}

func TestResolveWorkspaceFileRejectsEscape(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DataDir: root}
	workspace := ContextWorkspaceDir(cfg, "ctx")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := ResolveWorkspaceFile(cfg, "ctx", "hello.txt", 100)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "hello.txt" {
		t.Fatalf("got %q", path)
	}
	if _, err := ResolveWorkspaceFile(cfg, "ctx", "../outside.txt", 100); err == nil {
		t.Fatal("expected relative escape rejection")
	}
	if _, err := ResolveWorkspaceFile(cfg, "ctx", filepath.Join(workspace, "hello.txt"), 100); err == nil {
		t.Fatal("expected absolute path rejection")
	}
}

func TestResolveWorkspaceFileRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DataDir: root}
	workspace := ContextWorkspaceDir(cfg, "ctx")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWorkspaceFile(cfg, "ctx", "link.txt", 100); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}
