package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetMessageReactionPayload(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/setMessageReaction" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	client := NewTelegramClient("token")
	client.base = server.URL
	if err := client.SetMessageReaction(context.Background(), 123, 456, "👀"); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"].(float64) != 123 || got["message_id"].(float64) != 456 {
		t.Fatalf("unexpected ids: %#v", got)
	}
	reactions := got["reaction"].([]any)
	reaction := reactions[0].(map[string]any)
	if reaction["type"] != "emoji" || reaction["emoji"] != "👀" {
		t.Fatalf("unexpected reaction: %#v", reaction)
	}
}

func TestSetMyCommandsPayload(t *testing.T) {
	var got struct {
		Commands []TelegramBotCommand `json:"commands"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/setMyCommands" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	client := NewTelegramClient("token")
	client.base = server.URL
	commands := []TelegramBotCommand{
		{Command: "help", Description: "List commands"},
		{Command: "run", Description: "Run a prompt"},
	}
	if err := client.SetMyCommands(context.Background(), commands); err != nil {
		t.Fatal(err)
	}
	if len(got.Commands) != len(commands) {
		t.Fatalf("expected %d commands, got %#v", len(commands), got.Commands)
	}
	if got.Commands[0] != commands[0] || got.Commands[1] != commands[1] {
		t.Fatalf("unexpected commands: %#v", got.Commands)
	}
}

func TestGetMyCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getMyCommands" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"command":"help","description":"List commands"}]}`))
	}))
	defer server.Close()

	client := NewTelegramClient("token")
	client.base = server.URL
	commands, err := client.GetMyCommands(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Command != "help" || commands[0].Description != "List commands" {
		t.Fatalf("unexpected commands: %#v", commands)
	}
}

func TestTelegramBotCommandsAreValidForBotAPI(t *testing.T) {
	commands := telegramBotCommands()
	if len(commands) == 0 {
		t.Fatal("expected commands")
	}
	seen := map[string]bool{}
	for _, cmd := range commands {
		if cmd.Command == "" || len(cmd.Command) > 32 {
			t.Fatalf("invalid command length: %q", cmd.Command)
		}
		if strings.HasPrefix(cmd.Command, "/") {
			t.Fatalf("command must not include slash: %q", cmd.Command)
		}
		if cmd.Command != strings.ToLower(cmd.Command) {
			t.Fatalf("command must be lowercase: %q", cmd.Command)
		}
		for _, r := range cmd.Command {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				t.Fatalf("command has invalid character: %q", cmd.Command)
			}
		}
		if cmd.Description == "" || len(cmd.Description) > 256 {
			t.Fatalf("invalid description for %q: %q", cmd.Command, cmd.Description)
		}
		if seen[cmd.Command] {
			t.Fatalf("duplicate command: %q", cmd.Command)
		}
		seen[cmd.Command] = true
	}
	for _, required := range []string{"help", "run", "status", "cancel", "retry", "contexts", "switch", "renamectx", "synccommands", "showcommands"} {
		if !seen[required] {
			t.Fatalf("missing command %q", required)
		}
	}
}
