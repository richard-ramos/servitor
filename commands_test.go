package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdReasoningUpdatesContextAndConfig(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		CodexAuthMode:          AuthModeAPIKey,
		DataDir:                filepath.Join(root, "data"),
		DefaultCodexModel:      "gpt-test",
		DefaultReasoningEffort: defaultReasoningEffort,
		OpenAIProxyPort:        3021,
	}
	ctx := context.Background()
	db, err := OpenDB(filepath.Join(root, "servitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := Context{
		ID:              "ctx_test",
		Kind:            ContextKindScratch,
		State:           ContextStateActive,
		WorkspaceDir:    ContextWorkspaceDir(cfg, "ctx_test"),
		ReasoningEffort: defaultReasoningEffort,
	}
	if err := os.MkdirAll(ContextCodexDir(cfg, c.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CreateContext(ctx, db, c); err != nil {
		t.Fatal(err)
	}
	if err := BindTopic(ctx, db, 10, 20, c.ID); err != nil {
		t.Fatal(err)
	}

	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, body["text"].(string))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99,"message_thread_id":20,"chat":{"id":10},"from":{"id":1,"is_bot":true},"text":"ok"}}`))
	}))
	defer server.Close()

	app := &App{cfg: cfg, db: db, tg: NewTelegramClient("token"), redactor: NewRedactor("", "")}
	app.tg.base = server.URL
	msg := TelegramMessage{Chat: TelegramChat{ID: 10}, MessageThreadID: 20}
	if err := app.cmdReasoning(ctx, msg, "high"); err != nil {
		t.Fatal(err)
	}
	got, err := GetContextByID(ctx, db, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReasoningEffort != "high" {
		t.Fatalf("got reasoning effort %q want high", got.ReasoningEffort)
	}
	data, err := os.ReadFile(filepath.Join(ContextCodexDir(cfg, c.ID), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `model_reasoning_effort = "high"`) {
		t.Fatalf("config did not contain high reasoning effort:\n%s", string(data))
	}
	if len(replies) != 1 || !strings.Contains(replies[0], "model_reasoning_effort set to high") {
		t.Fatalf("unexpected replies: %#v", replies)
	}
}

func TestCmdRenameAndSwitchContext(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DataDir: filepath.Join(root, "data")}
	ctx := context.Background()
	db, err := OpenDB(filepath.Join(root, "servitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := Context{ID: "ctx_first", Kind: ContextKindScratch, State: ContextStateActive, WorkspaceDir: "/tmp/first"}
	second := Context{ID: "ctx_second", DisplayName: "work", Kind: ContextKindScratch, State: ContextStateActive, WorkspaceDir: "/tmp/second"}
	if err := CreateContext(ctx, db, first); err != nil {
		t.Fatal(err)
	}
	if err := CreateContext(ctx, db, second); err != nil {
		t.Fatal(err)
	}
	if err := BindTopic(ctx, db, 10, 20, first.ID); err != nil {
		t.Fatal(err)
	}

	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		replies = append(replies, body["text"].(string))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":99,"message_thread_id":20,"chat":{"id":10},"from":{"id":1,"is_bot":true},"text":"ok"}}`))
	}))
	defer server.Close()

	app := &App{cfg: cfg, db: db, tg: NewTelegramClient("token"), redactor: NewRedactor("", "")}
	app.tg.base = server.URL
	msg := TelegramMessage{Chat: TelegramChat{ID: 10}, MessageThreadID: 20}
	if err := app.cmdRenameContext(ctx, msg, "personal"); err != nil {
		t.Fatal(err)
	}
	renamed, err := GetContextByID(ctx, db, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.DisplayName != "personal" {
		t.Fatalf("got display name %q want personal", renamed.DisplayName)
	}
	if err := app.cmdSwitch(ctx, msg, "work"); err != nil {
		t.Fatal(err)
	}
	bound, err := GetBoundContext(ctx, db, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ID != second.ID {
		t.Fatalf("got bound context %q want %q", bound.ID, second.ID)
	}
	if len(replies) != 2 || !strings.Contains(replies[0], "Renamed context") || !strings.Contains(replies[1], "Switched topic") {
		t.Fatalf("unexpected replies: %#v", replies)
	}
}
