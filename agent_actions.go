package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AgentActionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type AgentAction struct {
	Type            string              `json:"type"`
	Ref             string              `json:"ref"`
	Text            string              `json:"text"`
	Options         []AgentActionOption `json:"options"`
	TargetRef       string              `json:"target_ref"`
	Target          string              `json:"target"`
	Emoji           string              `json:"emoji"`
	TargetContextID string              `json:"target_context_id"`
	Prompt          string              `json:"prompt"`
	ScheduleOp      string              `json:"schedule_op"`
	Kind            string              `json:"kind"`
	Spec            string              `json:"spec"`
	ScriptPath      string              `json:"script_path"`
	ExpiresSeconds  int                 `json:"expires_seconds"`
	CreatedAt       string              `json:"created_at"`
}

type AgentActionAudit struct {
	Accepted bool        `json:"accepted"`
	Reason   string      `json:"reason"`
	Action   AgentAction `json:"action"`
	At       string      `json:"at"`
}

type AgentActionHandler func(context.Context, AgentAction) (string, bool)

func (r *DockerRunner) monitorAgentActions(ctx context.Context, handlerCtx context.Context, path string, handler AgentActionHandler) {
	if handler == nil {
		return
	}
	auditPath := filepath.Join(filepath.Dir(path), "agent_actions_audit.jsonl")
	ticker := time.NewTicker(time.Duration(r.cfg.AgentMessagePollIntervalMS) * time.Millisecond)
	defer ticker.Stop()
	processed := 0
	for {
		select {
		case <-ctx.Done():
			r.processAgentActionFile(handlerCtx, path, auditPath, &processed, handler)
			return
		case <-ticker.C:
			r.processAgentActionFile(handlerCtx, path, auditPath, &processed, handler)
		}
	}
}

func (r *DockerRunner) processAgentActionFile(ctx context.Context, path, auditPath string, processed *int, handler AgentActionHandler) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if len(data) > 1024*1024 {
		_ = appendAgentActionAudit(auditPath, AgentAction{Type: "unknown"}, false, "outbox_too_large")
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
		var action AgentAction
		if err := json.Unmarshal([]byte(line), &action); err != nil {
			_ = appendAgentActionAudit(auditPath, AgentAction{}, false, "invalid_json")
			continue
		}
		action, reason, ok := r.validateAgentAction(action)
		if !ok {
			_ = appendAgentActionAudit(auditPath, action, false, reason)
			continue
		}
		deliveryReason, delivered := handler(ctx, action)
		if deliveryReason == "" {
			deliveryReason = "delivered"
		}
		_ = appendAgentActionAudit(auditPath, action, delivered, deliveryReason)
	}
}

func (r *DockerRunner) validateAgentAction(action AgentAction) (AgentAction, string, bool) {
	action.Type = strings.TrimSpace(action.Type)
	action.Ref = strings.TrimSpace(action.Ref)
	action.Text = strings.TrimSpace(action.Text)
	action.TargetRef = strings.TrimSpace(action.TargetRef)
	action.Target = strings.TrimSpace(action.Target)
	action.TargetContextID = strings.TrimSpace(action.TargetContextID)
	action.Prompt = strings.TrimSpace(action.Prompt)
	action.ScheduleOp = strings.TrimSpace(action.ScheduleOp)
	action.Kind = strings.TrimSpace(action.Kind)
	action.Spec = strings.TrimSpace(action.Spec)
	action.ScriptPath = strings.TrimSpace(action.ScriptPath)
	if action.ExpiresSeconds <= 0 {
		action.ExpiresSeconds = 3600
	}
	if action.ExpiresSeconds > 86400 {
		action.ExpiresSeconds = 86400
	}
	if action.Ref == "" {
		action.Ref = NewID("ref")
	}
	if len(action.Ref) > 40 || strings.ContainsAny(action.Ref, "\x00 ,:/\\") {
		return action, "invalid_ref", false
	}
	switch action.Type {
	case "interactive_question":
		if action.Text == "" || len(action.Options) < 1 || len(action.Options) > 6 {
			return action, "invalid_question", false
		}
		for i := range action.Options {
			action.Options[i].ID = strings.TrimSpace(action.Options[i].ID)
			action.Options[i].Label = strings.TrimSpace(action.Options[i].Label)
			if action.Options[i].ID == "" || len(action.Options[i].ID) > 12 || strings.ContainsAny(action.Options[i].ID, "\x00 ,:/\\") || action.Options[i].Label == "" {
				return action, "invalid_option", false
			}
		}
	case "edit":
		if action.TargetRef == "" || action.Text == "" {
			return action, "invalid_edit", false
		}
	case "reaction":
		if action.Emoji == "" || (action.Target != "source" && action.TargetRef == "") {
			return action, "invalid_reaction", false
		}
	case "schedule":
		if action.ScheduleOp == "" {
			return action, "invalid_schedule_op", false
		}
	case "agent_to_agent":
		if action.TargetContextID == "" || action.Prompt == "" {
			return action, "invalid_agent_to_agent", false
		}
	default:
		return action, "unsupported_type", false
	}
	return action, "accepted", true
}

func appendAgentActionAudit(path string, action AgentAction, accepted bool, reason string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(AgentActionAudit{
		Accepted: accepted,
		Reason:   reason,
		Action:   action,
		At:       time.Now().UTC().Format(time.RFC3339),
	})
}

func agentActionInstruction() string {
	return fmt.Sprintf("For host-validated interactive actions, use servitor-action ask/edit/react/schedule/message-context. These requests are audited and may be rejected by the host.\n")
}
