package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func BuildPrompt(ctx context.Context, db *sql.DB, cfg Config, c Context, chatID int64, topicID int, current StoredMessage, prompt string) (string, error) {
	msgs, err := RecentMessages(ctx, db, chatID, topicID, cfg.MaxHistoryMessages)
	if err != nil {
		return "", err
	}
	if current.ReplyToMessageID != 0 {
		reply, err := MessageByTelegramID(ctx, db, chatID, current.ReplyToMessageID)
		if err == nil && !containsMessage(msgs, reply.ID) {
			msgs = append([]StoredMessage{reply}, msgs...)
		}
	}
	var ids []int64
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	attachments, err := AttachmentsForMessages(ctx, db, ids)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("You are Codex running inside a Servitor-managed isolated Docker container.\n")
	b.WriteString("Only files under /home/agent/workspace are durable workspace files. Real host secrets are unavailable.\n")
	b.WriteString("If host actions are needed, use only explicitly documented Servitor tools or request the action in your final response.\n\n")
	if instruction := agentMessageInstruction(cfg); instruction != "" {
		b.WriteString("Controlled Telegram update tool:\n")
		b.WriteString(instruction)
		b.WriteString("\n")
	}
	b.WriteString("Controlled host action tool:\n")
	b.WriteString(agentActionInstruction())
	b.WriteString("\n")
	if instructions, err := os.ReadFile(filepath.Join(ContextDir(cfg, c.ID), "instructions.md")); err == nil && len(strings.TrimSpace(string(instructions))) > 0 {
		b.WriteString("Context instructions:\n")
		b.WriteString(strings.TrimSpace(string(instructions)))
		b.WriteString("\n\n")
	}
	b.WriteString("Recent Telegram topic history:\n")
	for _, m := range msgs {
		body := strings.TrimSpace(m.Text)
		if body == "" {
			body = strings.TrimSpace(m.Caption)
		}
		if body == "" {
			body = "(attachment or empty message)"
		}
		trust := ""
		if !m.IsAdmin && !m.IsBot {
			trust = " [UNTRUSTED non-admin]"
		}
		b.WriteString(fmt.Sprintf("[%s msg:%d%s] %s: %s\n", m.CreatedAt.Format("2006-01-02 15:04"), m.TelegramMessageID, trust, m.SenderName, body))
		for _, a := range attachments[m.ID] {
			b.WriteString(fmt.Sprintf("  attachment: /home/agent/workspace/%s", filepath.ToSlash(a.WorkspaceRelPath)))
			if a.OriginalFilename != "" {
				b.WriteString(fmt.Sprintf(" original=%q", a.OriginalFilename))
			}
			if a.MimeType != "" {
				b.WriteString(fmt.Sprintf(" mime=%s", a.MimeType))
			}
			b.WriteString(fmt.Sprintf(" size=%d\n", a.SizeBytes))
		}
	}
	b.WriteString("\nCurrent user request:\n")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n")
	return b.String(), nil
}

func containsMessage(msgs []StoredMessage, id int64) bool {
	for _, m := range msgs {
		if m.ID == id {
			return true
		}
	}
	return false
}
