package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const callbackPrefix = "sa"

func (a *App) handleAgentAction(ctx context.Context, runID int64, c Context, chatID int64, topicID int, sourceMessageID int, action AgentAction) (string, bool) {
	switch action.Type {
	case "interactive_question":
		if err := a.createQuestionAction(ctx, runID, c.ID, chatID, topicID, sourceMessageID, action, "interactive_question"); err != nil {
			return err.Error(), false
		}
		return "queued_question", true
	case "edit":
		target, err := OutboundActionByRef(ctx, a.db, c.ID, runID, action.TargetRef)
		if err != nil {
			return "target_not_found", false
		}
		if target.TelegramMessageID == 0 {
			return "target_has_no_message", false
		}
		if err := a.tg.EditMessageText(ctx, chatID, target.TelegramMessageID, action.Text, nil); err != nil {
			return "telegram_edit_failed", false
		}
		_ = AddOutboundActionEvent(ctx, a.db, OutboundActionEvent{ActionID: target.ID, UserID: 0, Value: "edit", Accepted: true, Reason: "agent_edit"})
		_ = a.recordImmediateAction(ctx, runID, c.ID, chatID, topicID, sourceMessageID, action, "completed")
		return "edited", true
	case "reaction":
		targetMsg := sourceMessageID
		if action.TargetRef != "" {
			target, err := OutboundActionByRef(ctx, a.db, c.ID, runID, action.TargetRef)
			if err != nil {
				return "target_not_found", false
			}
			targetMsg = target.TelegramMessageID
		}
		if targetMsg == 0 {
			return "target_has_no_message", false
		}
		if err := a.tg.SetMessageReaction(ctx, chatID, targetMsg, action.Emoji); err != nil {
			return "telegram_reaction_failed", false
		}
		_ = a.recordImmediateAction(ctx, runID, c.ID, chatID, topicID, sourceMessageID, action, "completed")
		return "reacted", true
	case "schedule":
		action.Text = scheduleApprovalText(action)
		action.Options = []AgentActionOption{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}}
		if err := a.createQuestionAction(ctx, runID, c.ID, chatID, topicID, sourceMessageID, action, "schedule_approval"); err != nil {
			return err.Error(), false
		}
		return "queued_schedule_approval", true
	case "agent_to_agent":
		targetBinding, err := BindingByContext(ctx, a.db, action.TargetContextID)
		if err != nil {
			return "target_context_not_bound", false
		}
		if targetBinding.ChatID != chatID {
			return "target_context_not_same_chat", false
		}
		prompt := fmt.Sprintf("Message from context %s run #%d:\n\n%s", c.ID, runID, action.Prompt)
		if _, err := Enqueue(ctx, a.db, action.TargetContextID, 0, 0, prompt, false); err != nil {
			return "enqueue_failed", false
		}
		_ = a.recordImmediateAction(ctx, runID, c.ID, chatID, topicID, sourceMessageID, action, "completed")
		return "enqueued_target_context", true
	default:
		return "unsupported_type", false
	}
}

func (a *App) recordImmediateAction(ctx context.Context, runID int64, contextID string, chatID int64, topicID int, sourceMessageID int, action AgentAction, status string) error {
	payload, _ := json.Marshal(action)
	return CreateOutboundAction(ctx, a.db, OutboundAction{
		ID:              NewID("act"),
		RunID:           runID,
		ContextID:       contextID,
		ChatID:          chatID,
		TopicID:         topicID,
		SourceMessageID: sourceMessageID,
		Type:            action.Type,
		Ref:             action.Ref,
		PayloadJSON:     string(payload),
		Status:          status,
		RequiresAdmin:   true,
	})
}

func (a *App) createQuestionAction(ctx context.Context, runID int64, contextID string, chatID int64, topicID int, sourceMessageID int, action AgentAction, actionType string) error {
	id := NewID("act")
	payload, _ := json.Marshal(action)
	expires := time.Now().UTC().Add(time.Duration(action.ExpiresSeconds) * time.Second)
	record := OutboundAction{
		ID:              id,
		RunID:           runID,
		ContextID:       contextID,
		ChatID:          chatID,
		TopicID:         topicID,
		SourceMessageID: sourceMessageID,
		Type:            actionType,
		Ref:             action.Ref,
		PayloadJSON:     string(payload),
		Status:          "pending",
		RequiresAdmin:   true,
		ExpiresAt:       &expires,
	}
	if err := CreateOutboundAction(ctx, a.db, record); err != nil {
		return err
	}
	markup, err := a.inlineKeyboardForAction(id, action.Options)
	if err != nil {
		return err
	}
	sent, err := a.tg.SendMessageWithInlineKeyboard(ctx, chatID, topicID, action.Text, markup)
	if err != nil {
		return err
	}
	if err := UpdateOutboundActionTelegramMessage(ctx, a.db, id, sent.MessageID); err != nil {
		return err
	}
	_, _ = StoreMessage(ctx, a.db, StoredMessage{
		ChatID:            sent.Chat.ID,
		TopicID:           sent.MessageThreadID,
		TelegramMessageID: sent.MessageID,
		SenderID:          sent.From.ID,
		SenderName:        "servitor",
		Text:              sent.Text,
		IsBot:             true,
		IsAdmin:           true,
	})
	return nil
}

func (a *App) inlineKeyboardForAction(actionID string, options []AgentActionOption) (InlineKeyboardMarkup, error) {
	var row []InlineKeyboardButton
	for _, opt := range options {
		token, err := a.signCallbackData(actionID, opt.ID)
		if err != nil {
			return InlineKeyboardMarkup{}, err
		}
		row = append(row, InlineKeyboardButton{Text: opt.Label, CallbackData: token})
	}
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{row}}, nil
}

func (a *App) handleCallbackQuery(ctx context.Context, upd Update) error {
	cb := upd.CallbackQuery
	actionID, value, ok := a.verifyCallbackData(cb.Data)
	if !ok {
		_ = a.tg.AnswerCallbackQuery(ctx, cb.ID, "Invalid action.")
		return nil
	}
	action, err := GetOutboundAction(ctx, a.db, actionID)
	if err != nil {
		_ = a.tg.AnswerCallbackQuery(ctx, cb.ID, "Action not found.")
		return nil
	}
	if action.Status != "pending" {
		_ = a.tg.AnswerCallbackQuery(ctx, cb.ID, "Action already completed.")
		return nil
	}
	if action.ExpiresAt != nil && time.Now().After(*action.ExpiresAt) {
		_ = CompleteOutboundAction(ctx, a.db, action.ID, "expired")
		_ = AddOutboundActionEvent(ctx, a.db, OutboundActionEvent{ActionID: action.ID, TelegramUpdateID: upd.UpdateID, UserID: cb.From.ID, Value: value, Accepted: false, Reason: "expired"})
		_ = a.tg.AnswerCallbackQuery(ctx, cb.ID, "Action expired.")
		return nil
	}
	if action.RequiresAdmin && !a.cfg.AdminUserIDs[cb.From.ID] {
		_ = AddOutboundActionEvent(ctx, a.db, OutboundActionEvent{ActionID: action.ID, TelegramUpdateID: upd.UpdateID, UserID: cb.From.ID, Value: value, Accepted: false, Reason: "not_admin"})
		_ = a.tg.AnswerCallbackQuery(ctx, cb.ID, "Not authorized.")
		return nil
	}
	accepted := value != "reject"
	reason := "accepted"
	if !accepted {
		reason = "rejected"
	}
	if err := AddOutboundActionEvent(ctx, a.db, OutboundActionEvent{ActionID: action.ID, TelegramUpdateID: upd.UpdateID, UserID: cb.From.ID, Value: value, Accepted: accepted, Reason: reason}); err != nil {
		return err
	}
	if !accepted {
		_ = CompleteOutboundAction(ctx, a.db, action.ID, "rejected")
		_ = a.tg.EditMessageText(ctx, action.ChatID, action.TelegramMessageID, fmt.Sprintf("Rejected by %s.", TelegramSenderName(cb.From)), nil)
		_ = a.tg.AnswerCallbackQuery(ctx, cb.ID, "Rejected.")
		return nil
	}
	continuation := fmt.Sprintf("Interactive response for %q: %s selected by %s. Continue from the prior task.", action.Ref, value, TelegramSenderName(cb.From))
	if action.Type == "schedule_approval" {
		summary, err := a.applyApprovedScheduleAction(ctx, action)
		if err != nil {
			_ = a.tg.AnswerCallbackQuery(ctx, cb.ID, "Failed: "+err.Error())
			return nil
		}
		continuation = "Schedule approval result: " + summary
	}
	if _, err := Enqueue(ctx, a.db, action.ContextID, 0, 0, continuation, false); err != nil {
		return err
	}
	_ = CompleteOutboundAction(ctx, a.db, action.ID, "completed")
	_ = a.tg.EditMessageText(ctx, action.ChatID, action.TelegramMessageID, fmt.Sprintf("Accepted by %s: %s", TelegramSenderName(cb.From), value), nil)
	_ = a.tg.AnswerCallbackQuery(ctx, cb.ID, "Accepted.")
	return nil
}

func (a *App) applyApprovedScheduleAction(ctx context.Context, action OutboundAction) (string, error) {
	var req AgentAction
	if err := json.Unmarshal([]byte(action.PayloadJSON), &req); err != nil {
		return "", err
	}
	switch req.ScheduleOp {
	case "create":
		s, err := InitialNextRun(req.Kind, req.Spec, a.cfg.ServiceTimezone)
		if err != nil {
			return "", err
		}
		if req.ScriptPath != "" {
			if _, err := ResolveWorkspaceFile(a.cfg, action.ContextID, req.ScriptPath, 0); err != nil {
				return "", err
			}
		}
		s.ID = NewID("sch")
		s.ContextID = action.ContextID
		s.ScriptPath = req.ScriptPath
		s.Prompt = req.Prompt
		if err := CreateSchedule(ctx, a.db, s); err != nil {
			return "", err
		}
		return fmt.Sprintf("created schedule %s", s.ID), nil
	case "pause":
		return "paused schedule " + req.Target, UpdateScheduleStatus(ctx, a.db, req.Target, action.ContextID, ScheduleStatusPaused)
	case "resume":
		s, err := GetSchedule(ctx, a.db, req.Target, action.ContextID)
		if err != nil {
			return "", err
		}
		next, err := recomputeNextRun(s, a.cfg.ServiceTimezone)
		if err != nil {
			return "", err
		}
		s.Status = ScheduleStatusActive
		s.NextRunAt = next
		return "resumed schedule " + req.Target, UpdateSchedule(ctx, a.db, s)
	case "cancel":
		return "cancelled schedule " + req.Target, DeleteSchedule(ctx, a.db, req.Target, action.ContextID)
	default:
		return "", fmt.Errorf("unsupported schedule op %q", req.ScheduleOp)
	}
}

func scheduleApprovalText(action AgentAction) string {
	return fmt.Sprintf("Approve schedule action %q kind=%q spec=%q script=%q prompt=%q?", action.ScheduleOp, action.Kind, action.Spec, action.ScriptPath, action.Prompt)
}

func (a *App) signCallbackData(actionID, value string) (string, error) {
	key, err := a.callbackSigningKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(actionID + "\x00" + value))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:10])
	return callbackPrefix + ":" + actionID + ":" + value + ":" + sig, nil
}

func (a *App) verifyCallbackData(data string) (string, string, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 4 || parts[0] != callbackPrefix {
		return "", "", false
	}
	expected, err := a.signCallbackData(parts[1], parts[2])
	if err != nil {
		return "", "", false
	}
	return parts[1], parts[2], hmac.Equal([]byte(data), []byte(expected))
}

func (a *App) callbackSigningKey() ([]byte, error) {
	path := filepath.Join(a.cfg.DataDir, "callback_signing_key")
	if data, err := os.ReadFile(path); err == nil {
		decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err == nil && len(decoded) >= 32 {
			return decoded, nil
		}
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key[:])), 0o600); err != nil {
		return nil, err
	}
	return key[:], nil
}
