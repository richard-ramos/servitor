package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const maxRetries = 3

type activeRun struct {
	RunID     int64
	QueueID   int64
	ContextID string
	ChatID    int64
	TopicID   int
	Stage     string
	StartedAt time.Time
	Cancel    context.CancelFunc
}

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

func (a *App) registerActiveRun(run activeRun) {
	a.activeRunMu.Lock()
	defer a.activeRunMu.Unlock()
	if a.activeRuns == nil {
		a.activeRuns = map[int64]activeRun{}
	}
	if a.activeContext == nil {
		a.activeContext = map[string]int64{}
	}
	a.activeRuns[run.RunID] = run
	a.activeContext[run.ContextID] = run.RunID
}

func (a *App) unregisterActiveRun(runID int64) {
	a.activeRunMu.Lock()
	defer a.activeRunMu.Unlock()
	run, ok := a.activeRuns[runID]
	if !ok {
		return
	}
	delete(a.activeRuns, runID)
	if a.activeContext[run.ContextID] == runID {
		delete(a.activeContext, run.ContextID)
	}
}

func (a *App) activeRunForContext(contextID string) (activeRun, bool) {
	a.activeRunMu.Lock()
	defer a.activeRunMu.Unlock()
	runID, ok := a.activeContext[contextID]
	if !ok {
		return activeRun{}, false
	}
	run, ok := a.activeRuns[runID]
	return run, ok
}

func (a *App) activeRunByID(runID int64) (activeRun, bool) {
	a.activeRunMu.Lock()
	defer a.activeRunMu.Unlock()
	run, ok := a.activeRuns[runID]
	return run, ok
}

func (a *App) cancelActiveRunForContext(contextID string) (activeRun, bool) {
	run, ok := a.activeRunForContext(contextID)
	if !ok {
		return activeRun{}, false
	}
	run.Cancel()
	return run, true
}

func (a *App) setActiveRunStage(runID int64, stage string) {
	a.activeRunMu.Lock()
	defer a.activeRunMu.Unlock()
	run, ok := a.activeRuns[runID]
	if !ok {
		return
	}
	run.Stage = stage
	a.activeRuns[runID] = run
}

func (a *App) runProgress(ctx context.Context, chatID int64, topicID int, runID int64, stage string) {
	a.setActiveRunStage(runID, stage)
	a.sendProgress(ctx, chatID, topicID, fmt.Sprintf("Run #%d: %s.", runID, stage))
}

func (a *App) processQueueItem(ctx context.Context, item QueueItem) (err error) {
	defer func() {
		if err != nil {
			_ = MarkQueueFailed(ctx, a.db, item.ID, "host_error", false, maxRetries)
		}
	}()
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
	a.sendProgress(ctx, chatID, topicID, fmt.Sprintf("Preparing context %s.", c.ID))
	if err := PrepareContextCodexAssets(ctx, a.db, a.cfg, c); err != nil {
		return err
	}
	a.sendProgress(ctx, chatID, topicID, "Building prompt.")
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
	runCtx, cancelRun := context.WithCancel(ctx)
	a.registerActiveRun(activeRun{
		RunID:     runID,
		QueueID:   item.ID,
		ContextID: c.ID,
		ChatID:    chatID,
		TopicID:   topicID,
		Stage:     "starting Codex",
		StartedAt: time.Now(),
		Cancel:    cancelRun,
	})
	defer func() {
		cancelRun()
		a.unregisterActiveRun(runID)
	}()
	progressDone := make(chan struct{})
	go a.runProgressTicker(ctx, progressDone, chatID, topicID, runID)
	session := ""
	if item.Resume {
		session = c.CodexSession
	}
	a.runProgress(ctx, chatID, topicID, runID, "running Codex")
	result := a.runner.Run(runCtx, runID, c, fullPrompt, session, func(messageCtx context.Context, msg AgentMessage) (string, bool) {
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
	}, func(actionCtx context.Context, action AgentAction) (string, bool) {
		return a.handleAgentAction(actionCtx, runID, c, chatID, topicID, current.TelegramMessageID, action)
	})
	close(progressDone)
	a.runProgress(ctx, chatID, topicID, runID, "collecting result")
	if result.Canceled {
		if err := FinishRun(ctx, a.db, runID, result, "cancelled"); err != nil {
			return err
		}
		if err := MarkQueueCancelled(ctx, a.db, item.ID); err != nil {
			return err
		}
		a.sendProgress(ctx, chatID, topicID, fmt.Sprintf("Cancelled run #%d.", runID))
		return nil
	}
	status := "done"
	if result.ExitCode != 0 || result.ErrorText != "" {
		status = "failed"
	}
	if err := FinishRun(ctx, a.db, runID, result, status); err != nil {
		return err
	}
	if result.ResponsePath != "" {
		usage := ParseUsageFromJSONL(result.ResponsePath)
		usage.RunID = runID
		usage.QueueID = item.ID
		usage.ContextID = c.ID
		_ = AddUsageRecord(ctx, a.db, usage)
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
			stage := "running"
			if run, ok := a.activeRunByID(runID); ok && run.Stage != "" {
				stage = run.Stage
			}
			a.sendProgress(ctx, chatID, topicID, fmt.Sprintf("Run #%d is still %s after %s.", runID, stage, time.Since(start).Round(time.Second)))
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
