package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCodexAccessToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"test-access","refresh_token":"test-refresh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := readCodexAccessToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if token != "test-access" {
		t.Fatalf("got %q want test-access", token)
	}
}

func TestExpandPathHome(t *testing.T) {
	got, err := expandPath("~/.codex")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("expected absolute path, got %q", got)
	}
}
