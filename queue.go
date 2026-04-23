package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const maxRetries = 3

func (a *App) RunQueueLoop(ctx context.Context) {
	_ = ResetStaleRunning(ctx, a.db)
	sem := make(chan struct{}, a.cfg.MaxConcurrentContainers)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				select {
				case sem <- struct{}{}:
				default:
					goto drained
				}
				item, err := ClaimNextPending(ctx, a.db)
				if err != nil {
					<-sem
					if !isNoRows(err) {
						fmt.Printf("queue fetch error: %s\n", a.redactor.Redact(err.Error()))
					}
					goto drained
				}
				go func(q QueueItem) {
					defer func() { <-sem }()
					if err := a.processQueueItem(ctx, q); err != nil {
						fmt.Printf("queue item %d: %s\n", q.ID, a.redactor.Redact(err.Error()))
					}
				}(item)
			}
		drained:
		}
	}
}

func (a *App) processQueueItem(ctx context.Context, item QueueItem) error {
	c, err := GetContextByID(ctx, a.db, item.ContextID)
	if err != nil {
		return err
	}
	var current StoredMessage
	var chatID int64
	var topicID int
	if item.MessageID != 0 {
		current, err = MessageByID(ctx, a.db, item.MessageID)
		if err != nil {
			return err
		}
		chatID, topicID = current.ChatID, current.TopicID
	} else {
		b, err := BindingByContext(ctx, a.db, item.ContextID)
		if err != nil {
			return err
		}
		chatID, topicID = b.ChatID, b.TopicID
		current = StoredMessage{
			ID:                0,
			ChatID:            chatID,
			TopicID:           topicID,
			TelegramMessageID: 0,
			SenderName:        "servitor-scheduler",
			Text:              item.Prompt,
			IsAdmin:           true,
			CreatedAt:         time.Now(),
		}
	}
	if c.State == ContextStateArchived {
		_ = MarkQueueFailed(ctx, a.db, item.ID, "context_archived", false, maxRetries)
		return nil
	}
	fullPrompt, err := BuildPrompt(ctx, a.db, a.cfg, c, chatID, topicID, current, item.Prompt)
	if err != nil {
		return err
	}
	runID, err := CreateRun(ctx, a.db, item.ID, item.ContextID, "")
	if err != nil {
		return err
	}
	if err := MarkQueueRunning(ctx, a.db, item.ID, runID); err != nil {
		return err
	}
	a.sendProgress(ctx, chatID, topicID, fmt.Sprintf("Started run #%d for context %s.", runID, c.ID))
	progressDone := make(chan struct{})
	go a.runProgressTicker(ctx, progressDone, chatID, topicID, runID)
	session := ""
	if item.Resume {
		session = c.CodexSession
	}
	result := a.runner.Run(ctx, runID, c, fullPrompt, session, func(messageCtx context.Context, msg AgentMessage) (string, bool) {
		switch msg.Type {
		case "telegram_message":
			if _, err := a.reply(messageCtx, chatID, topicID, msg.Text); err != nil {
				return "telegram_send_failed", false
			}
			return "delivered", true
		case "telegram_file":
			path, err := ResolveWorkspaceFile(a.cfg, c.ID, msg.Path, a.cfg.MaxAttachmentBytes)
			if err != nil {
				return "file_rejected: " + err.Error(), false
			}
			sent, err := a.tg.SendDocument(messageCtx, chatID, topicID, path, msg.Caption)
			if err != nil {
				return "telegram_file_send_failed", false
			}
			_, _ = StoreMessage(messageCtx, a.db, StoredMessage{
				ChatID:            sent.Chat.ID,
				TopicID:           sent.MessageThreadID,
				TelegramMessageID: sent.MessageID,
				SenderID:          sent.From.ID,
				SenderName:        "servitor",
				Caption:           sent.Caption,
				IsBot:             true,
				IsAdmin:           true,
			})
			return "delivered", true
		default:
			return "unsupported_type", false
		}
	})
	close(progressDone)
	status := "done"
	if result.ExitCode != 0 || result.ErrorText != "" {
		status = "failed"
	}
	if err := FinishRun(ctx, a.db, runID, result, status); err != nil {
		return err
	}
	if result.SessionID != "" {
		_ = UpdateContextSession(ctx, a.db, c.ID, result.SessionID)
	}
	if status == "done" {
		if err := MarkQueueDone(ctx, a.db, item.ID); err != nil {
			return err
		}
		reply := result.LastMessage
		if strings.TrimSpace(reply) == "" {
			reply = "Codex completed without a final message."
		}
		a.sendProgress(ctx, chatID, topicID, fmt.Sprintf("Completed run #%d.", runID))
		_, _ = a.reply(ctx, chatID, topicID, reply)
		return nil
	}
	willRetry := result.Retryable && item.Attempts+1 < maxRetries
	if err := MarkQueueFailed(ctx, a.db, item.ID, result.FailureClass, result.Retryable, maxRetries); err != nil {
		return err
	}
	if !willRetry {
		_, _ = a.reply(ctx, chatID, topicID, "Codex run failed: "+a.redactor.Redact(result.ErrorText))
	} else {
		a.sendProgress(ctx, chatID, topicID, fmt.Sprintf("Run #%d hit a retryable infrastructure failure: %s.", runID, result.FailureClass))
	}
	return nil
}

func (a *App) sendProgress(ctx context.Context, chatID int64, topicID int, text string) {
	if !a.cfg.ProgressUpdates {
		return
	}
	_, _ = a.reply(ctx, chatID, topicID, text)
}

func (a *App) runProgressTicker(ctx context.Context, done <-chan struct{}, chatID int64, topicID int, runID int64) {
	if !a.cfg.ProgressUpdates {
		return
	}
	ticker := time.NewTicker(time.Duration(a.cfg.ProgressIntervalSeconds) * time.Second)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			a.sendProgress(ctx, chatID, topicID, fmt.Sprintf("Run #%d is still running after %s.", runID, time.Since(start).Round(time.Second)))
		}
	}
}

func MessageByID(ctx context.Context, db *sql.DB, id int64) (StoredMessage, error) {
	row := db.QueryRowContext(ctx, `SELECT id, chat_id, topic_id, telegram_msg_id, sender_id, sender_name, text, caption, reply_to_msg_id, is_bot, is_admin, created_at
		FROM messages WHERE id=?`, id)
	return scanMessage(row)
}

func BindingByContext(ctx context.Context, db *sql.DB, contextID string) (TopicBinding, error) {
	var b TopicBinding
	err := db.QueryRowContext(ctx, `SELECT chat_id, topic_id, context_id FROM topic_bindings WHERE context_id=? ORDER BY created_at DESC LIMIT 1`, contextID).Scan(&b.ChatID, &b.TopicID, &b.ContextID)
	return b, err
}
