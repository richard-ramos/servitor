package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type AgentMessage struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Path      string `json:"path"`
	Caption   string `json:"caption"`
	CreatedAt string `json:"created_at"`
}

type AgentMessageAudit struct {
	Accepted bool         `json:"accepted"`
	Reason   string       `json:"reason"`
	Message  AgentMessage `json:"message"`
	At       string       `json:"at"`
}

type AgentMessageHandler func(context.Context, AgentMessage) (string, bool)

func (r *DockerRunner) monitorAgentMessages(ctx context.Context, handlerCtx context.Context, path string, handler AgentMessageHandler) {
	if !r.cfg.AgentMessagesEnabled || handler == nil || r.cfg.AgentMessageMaxPerRun == 0 {
		return
	}
	auditPath := filepath.Join(filepath.Dir(path), "agent_messages_audit.jsonl")
	ticker := time.NewTicker(time.Duration(r.cfg.AgentMessagePollIntervalMS) * time.Millisecond)
	defer ticker.Stop()
	processed := 0
	accepted := 0
	for {
		select {
		case <-ctx.Done():
			r.processAgentMessageFile(handlerCtx, path, auditPath, &processed, &accepted, handler)
			return
		case <-ticker.C:
			r.processAgentMessageFile(handlerCtx, path, auditPath, &processed, &accepted, handler)
		}
	}
}

func (r *DockerRunner) processAgentMessageFile(ctx context.Context, path, auditPath string, processed *int, accepted *int, handler AgentMessageHandler) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if len(data) > 1024*1024 {
		_ = appendAgentMessageAudit(auditPath, AgentMessage{Type: "telegram_message"}, false, "outbox_too_large")
		return
	}
	text := string(data)
	lines := completeJSONLLines(text)
	for *processed < len(lines) {
		line := strings.TrimSpace(lines[*processed])
		*processed++
		if line == "" {
			continue
		}
		var msg AgentMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			_ = appendAgentMessageAudit(auditPath, AgentMessage{}, false, "invalid_json")
			continue
		}
		msg, reason, ok := r.validateAgentMessage(msg, *accepted)
		if !ok {
			_ = appendAgentMessageAudit(auditPath, msg, false, reason)
			continue
		}
		deliveryReason, delivered := handler(ctx, msg)
		if deliveryReason == "" {
			deliveryReason = "delivered"
		}
		_ = appendAgentMessageAudit(auditPath, msg, delivered, deliveryReason)
		if delivered {
			*accepted++
		}
	}
}

func completeJSONLLines(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return nil
	}
	// Drop the trailing empty segment for newline-terminated files, or the
	// trailing partial segment if the writer is mid-line.
	return lines[:len(lines)-1]
}

func (r *DockerRunner) validateAgentMessage(msg AgentMessage, accepted int) (AgentMessage, string, bool) {
	if msg.Type == "" {
		msg.Type = "telegram_message"
	}
	if accepted >= r.cfg.AgentMessageMaxPerRun {
		return msg, "message_limit_exceeded", false
	}
	switch msg.Type {
	case "telegram_message":
		return r.validateAgentTextMessage(msg)
	case "telegram_file":
		return r.validateAgentFileMessage(msg)
	default:
		return msg, "unsupported_type", false
	}
}

func (r *DockerRunner) validateAgentTextMessage(msg AgentMessage) (AgentMessage, string, bool) {
	msg.Text = strings.TrimSpace(msg.Text)
	if msg.Text == "" {
		return msg, "empty_text", false
	}
	if strings.HasPrefix(msg.Text, "/") {
		return msg, "telegram_commands_not_allowed", false
	}
	if utf8.RuneCountInString(msg.Text) > r.cfg.AgentMessageMaxChars {
		msg.Text = truncateRunes(msg.Text, r.cfg.AgentMessageMaxChars)
		return msg, "truncated", true
	}
	return msg, "accepted", true
}

func (r *DockerRunner) validateAgentFileMessage(msg AgentMessage) (AgentMessage, string, bool) {
	msg.Path = strings.TrimSpace(msg.Path)
	if msg.Path == "" {
		return msg, "empty_path", false
	}
	if strings.ContainsRune(msg.Path, 0) || strings.Contains(msg.Path, ":") || strings.Contains(msg.Path, ",") {
		return msg, "unsafe_path", false
	}
	if filepath.IsAbs(msg.Path) {
		return msg, "absolute_path_not_allowed", false
	}
	clean := filepath.Clean(msg.Path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return msg, "path_escape_not_allowed", false
	}
	msg.Path = clean
	msg.Caption = strings.TrimSpace(msg.Caption)
	if strings.HasPrefix(msg.Caption, "/") {
		return msg, "telegram_commands_not_allowed", false
	}
	if utf8.RuneCountInString(msg.Caption) > r.cfg.AgentMessageMaxChars {
		msg.Caption = truncateRunes(msg.Caption, r.cfg.AgentMessageMaxChars)
		return msg, "truncated", true
	}
	return msg, "accepted", true
}

func appendAgentMessageAudit(path string, msg AgentMessage, accepted bool, reason string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	audit := AgentMessageAudit{
		Accepted: accepted,
		Reason:   reason,
		Message:  msg,
		At:       time.Now().UTC().Format(time.RFC3339),
	}
	return json.NewEncoder(f).Encode(audit)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Split(bufio.ScanRunes)
	var b strings.Builder
	count := 0
	for scanner.Scan() {
		if count >= max {
			break
		}
		b.WriteString(scanner.Text())
		count++
	}
	return b.String()
}

func agentMessageInstruction(cfg Config) string {
	if !cfg.AgentMessagesEnabled || cfg.AgentMessageMaxPerRun == 0 {
		return ""
	}
	return fmt.Sprintf("During the run, you may send up to %d host-validated same-topic Telegram updates by running: servitor-send \"message text\". You may also ask the host to attach a workspace file by running: servitor-send-file <workspace-relative-path> [caption]. File paths must be relative to /home/agent/workspace. Do not use these helpers for Telegram commands, secrets, spam, or final answers; your normal final response is still required. Each request is audited.\n", cfg.AgentMessageMaxPerRun)
}
