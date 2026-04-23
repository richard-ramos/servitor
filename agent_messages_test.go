package main

import "testing"

func TestValidateAgentMessage(t *testing.T) {
	r := NewDockerRunner(Config{
		AgentMessagesEnabled:  true,
		AgentMessageMaxPerRun: 2,
		AgentMessageMaxChars:  5,
	}, NewRedactor())

	msg, reason, ok := r.validateAgentMessage(AgentMessage{Text: "hello"}, 0)
	if !ok || reason != "accepted" || msg.Text != "hello" {
		t.Fatalf("expected accepted hello, got ok=%t reason=%s msg=%q", ok, reason, msg.Text)
	}

	_, reason, ok = r.validateAgentMessage(AgentMessage{Text: "/archive"}, 0)
	if ok || reason != "telegram_commands_not_allowed" {
		t.Fatalf("expected command rejection, got ok=%t reason=%s", ok, reason)
	}

	msg, reason, ok = r.validateAgentMessage(AgentMessage{Text: "hello world"}, 0)
	if !ok || reason != "truncated" || msg.Text != "hello" {
		t.Fatalf("expected truncation, got ok=%t reason=%s msg=%q", ok, reason, msg.Text)
	}

	_, reason, ok = r.validateAgentMessage(AgentMessage{Text: "another"}, 2)
	if ok || reason != "message_limit_exceeded" {
		t.Fatalf("expected limit rejection, got ok=%t reason=%s", ok, reason)
	}
}

func TestValidateAgentFileMessage(t *testing.T) {
	r := NewDockerRunner(Config{
		AgentMessagesEnabled:  true,
		AgentMessageMaxPerRun: 2,
		AgentMessageMaxChars:  5,
	}, NewRedactor())

	msg, reason, ok := r.validateAgentMessage(AgentMessage{Type: "telegram_file", Path: "reports/hello.txt", Caption: "done"}, 0)
	if !ok || reason != "accepted" || msg.Path != "reports/hello.txt" {
		t.Fatalf("expected accepted file request, got ok=%t reason=%s msg=%+v", ok, reason, msg)
	}

	_, reason, ok = r.validateAgentMessage(AgentMessage{Type: "telegram_file", Path: "../secret.txt"}, 0)
	if ok || reason != "path_escape_not_allowed" {
		t.Fatalf("expected path escape rejection, got ok=%t reason=%s", ok, reason)
	}

	_, reason, ok = r.validateAgentMessage(AgentMessage{Type: "telegram_file", Path: "/tmp/secret.txt"}, 0)
	if ok || reason != "absolute_path_not_allowed" {
		t.Fatalf("expected absolute path rejection, got ok=%t reason=%s", ok, reason)
	}

	msg, reason, ok = r.validateAgentMessage(AgentMessage{Type: "telegram_file", Path: "hello.txt", Caption: "abcdef"}, 0)
	if !ok || reason != "truncated" || msg.Caption != "abcde" {
		t.Fatalf("expected caption truncation, got ok=%t reason=%s msg=%+v", ok, reason, msg)
	}
}
