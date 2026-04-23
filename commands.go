package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) dispatchMessage(ctx context.Context, msg TelegramMessage, messageID int64) error {
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		return a.handleCommand(ctx, msg, messageID, text)
	}
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound. Use /newctx scratch or /newctx repo <https-url>.")
			return nil
		}
		return err
	}
	if c.State == ContextStateArchived {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Context is archived.")
		return nil
	}
	if err := a.storeAttachmentsForMessage(ctx, c, messageID, msg); err != nil {
		return err
	}
	promptText := text
	if promptText == "" {
		promptText = msg.Caption
	}
	if strings.TrimSpace(promptText) == "" && len(attachmentCandidates(msg)) > 0 {
		promptText = "Please inspect the attached file(s)."
	}
	if strings.TrimSpace(promptText) == "" {
		return nil
	}
	if _, err := Enqueue(ctx, a.db, c.ID, messageID, msg.MessageID, promptText, false); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Queued.")
	return nil
}

func (a *App) handleCommand(ctx context.Context, msg TelegramMessage, messageID int64, raw string) error {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	args := strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
	switch cmd {
	case "/help":
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, helpText())
	case "/whoami":
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Telegram user ID: %d\nAdmin: %t", msg.From.ID, a.cfg.AdminUserIDs[msg.From.ID]))
	case "/newctx":
		return a.cmdNewContext(ctx, msg, args)
	case "/bind":
		return a.cmdBind(ctx, msg, args)
	case "/detach":
		if err := DetachTopic(ctx, a.db, msg.Chat.ID, msg.MessageThreadID); err != nil {
			return err
		}
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Detached topic.")
	case "/topicinfo":
		return a.cmdTopicInfo(ctx, msg)
	case "/explainctx":
		return a.cmdExplainContext(ctx, msg)
	case "/run":
		return a.cmdRun(ctx, msg, messageID, args, false)
	case "/resume":
		return a.cmdRun(ctx, msg, messageID, args, true)
	case "/archive":
		return a.cmdArchive(ctx, msg)
	case "/tail":
		return a.cmdTail(ctx, msg, args)
	case "/artifacts":
		return a.cmdArtifacts(ctx, msg)
	case "/sendfile":
		return a.cmdSendFile(ctx, msg, args)
	case "/loop":
		return a.cmdLoop(ctx, msg, args)
	case "/loops":
		return a.cmdLoops(ctx, msg)
	case "/unloop":
		return a.cmdUnloop(ctx, msg, args)
	default:
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Unknown command. Use /help.")
	}
	return nil
}

func (a *App) cmdNewContext(ctx context.Context, msg TelegramMessage, args string) error {
	parts := strings.Fields(args)
	if len(parts) < 1 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /newctx scratch OR /newctx repo <https-url>")
		return nil
	}
	kind := parts[0]
	if kind != ContextKindScratch && kind != ContextKindRepo {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Context kind must be scratch or repo.")
		return nil
	}
	repoURL := ""
	if kind == ContextKindRepo {
		if len(parts) < 2 {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /newctx repo <https-url>")
			return nil
		}
		repoURL = parts[1]
		if err := ValidatePublicHTTPSRepo(repoURL); err != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Invalid public HTTPS repo URL: "+err.Error())
			return nil
		}
	}
	id := NewID("ctx")
	if err := a.createContextDirs(id, kind, repoURL); err != nil {
		return err
	}
	c := Context{ID: id, Kind: kind, State: ContextStateActive, RepoURL: repoURL, WorkspaceDir: ContextWorkspaceDir(a.cfg, id)}
	if err := CreateContext(ctx, a.db, c); err != nil {
		return err
	}
	if err := BindTopic(ctx, a.db, msg.Chat.ID, msg.MessageThreadID, id); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Created and bound context %s (%s).", id, kind))
	return nil
}

func (a *App) createContextDirs(id, kind, repoURL string) error {
	for _, dir := range []string{ContextDir(a.cfg, id), ContextWorkspaceDir(a.cfg, id), ContextCodexDir(a.cfg, id), ContextRunsDir(a.cfg, id)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	instructions := "# Servitor Context Instructions\n\nThis is an isolated container workspace. Only files under `/home/agent/workspace` are durable project files. Secrets are unavailable inside the container. Host actions must be requested in the final response, not attempted directly.\n"
	if err := os.WriteFile(filepath.Join(ContextDir(a.cfg, id), "instructions.md"), []byte(instructions), 0o600); err != nil {
		return err
	}
	if err := writeCodexConfig(a.cfg, id); err != nil {
		return err
	}
	if kind == ContextKindRepo {
		parent := ContextWorkspaceDir(a.cfg, id)
		tmp := parent + ".clone"
		_ = os.RemoveAll(tmp)
		cmd := exec.Command("git", "clone", "--depth=1", repoURL, tmp)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git clone failed: %s: %w", string(out), err)
		}
		if err := os.RemoveAll(parent); err != nil {
			return err
		}
		if err := os.Rename(tmp, parent); err != nil {
			return err
		}
	}
	return nil
}

func writeCodexConfig(cfg Config, id string) error {
	dir := ContextCodexDir(cfg, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if cfg.CodexAuthMode == AuthModeChatGPT {
		if err := writePlaceholderCodexAuth(cfg, dir); err != nil {
			return err
		}
		config := fmt.Sprintf(`approval_policy = "never"
sandbox_mode = "danger-full-access"
model_provider = "servitor"
model = %q
model_reasoning_effort = %q

[model_providers.servitor]
name = "Servitor ChatGPT Proxy"
base_url = %q
wire_api = "responses"
requires_openai_auth = true

[projects."/home/agent/workspace"]
trust_level = "trusted"

[notice]
hide_full_access_warning = true
`, cfg.DefaultCodexModel, cfg.DefaultReasoningEffort, cfg.ContainerProxyChatGPTBaseURL())
		return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600)
	}
	config := fmt.Sprintf(`approval_policy = "never"
sandbox_mode = "danger-full-access"
model = %q
model_reasoning_effort = %q
openai_base_url = %q

[projects."/home/agent/workspace"]
trust_level = "trusted"

[notice]
hide_full_access_warning = true
`, cfg.DefaultCodexModel, cfg.DefaultReasoningEffort, cfg.ContainerProxyBaseURL())
	return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600)
}

func writePlaceholderCodexAuth(cfg Config, codexDir string) error {
	hostAuthDir, err := expandPath(cfg.CodexHostAuthDir)
	if err != nil {
		return err
	}
	accountID, err := readCodexAccountID(filepath.Join(hostAuthDir, "auth.json"))
	if err != nil {
		return err
	}
	auth := fmt.Sprintf(`{
  "auth_mode": "chatgpt",
  "tokens": {
    "access_token": %q,
    "id_token": "eyJhbGciOiJub25lIn0.eyJzdWIiOiJzZXJ2aXRvciJ9.c2ln",
    "refresh_token": "servitor-placeholder",
    "account_id": %q
  },
  "last_refresh": %q
}
`, cfg.OpenAIProxyClientToken, accountID, time.Now().UTC().Format(time.RFC3339))
	return os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(auth), 0o600)
}

func (a *App) cmdBind(ctx context.Context, msg TelegramMessage, args string) error {
	id := strings.TrimSpace(args)
	if id == "" {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /bind <context_id>")
		return nil
	}
	c, err := GetContextByID(ctx, a.db, id)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Context not found.")
			return nil
		}
		return err
	}
	if c.State == ContextStateArchived {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Context is archived.")
		return nil
	}
	if err := BindTopic(ctx, a.db, msg.Chat.ID, msg.MessageThreadID, id); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Bound context "+id+".")
	return nil
}

func (a *App) cmdTopicInfo(ctx context.Context, msg TelegramMessage) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Context: %s\nKind: %s\nState: %s\nWorkspace: %s", c.ID, c.Kind, c.State, c.WorkspaceDir))
	return nil
}

func (a *App) cmdExplainContext(ctx context.Context, msg TelegramMessage) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	run, err := LatestRunForContext(ctx, a.db, c.ID)
	latest := "none"
	if err == nil {
		latest = fmt.Sprintf("#%d %s exit=%d", run.ID, run.Status, run.ExitCode)
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Context: %s\nKind: %s\nState: %s\nRepo: %s\nWorkspace: %s\nSession: %s\nLatest run: %s", c.ID, c.Kind, c.State, c.RepoURL, c.WorkspaceDir, emptyDash(c.CodexSession), latest))
	return nil
}

func (a *App) cmdRun(ctx context.Context, msg TelegramMessage, messageID int64, args string, resume bool) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound. Use /newctx first.")
			return nil
		}
		return err
	}
	if c.State == ContextStateArchived {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Context is archived.")
		return nil
	}
	if strings.TrimSpace(args) == "" {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /run <prompt>")
		return nil
	}
	if err := a.storeAttachmentsForMessage(ctx, c, messageID, msg); err != nil {
		return err
	}
	if _, err := Enqueue(ctx, a.db, c.ID, messageID, msg.MessageID, args, resume); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Queued.")
	return nil
}

func (a *App) cmdArchive(ctx context.Context, msg TelegramMessage) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	if err := ArchiveContext(ctx, a.db, c.ID); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Archived context "+c.ID+".")
	return nil
}

func (a *App) cmdTail(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	run, err := LatestRunForContext(ctx, a.db, c.ID)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No runs yet.")
		return nil
	}
	n := 80
	if strings.TrimSpace(args) != "" {
		if parsed, err := strconv.Atoi(strings.TrimSpace(args)); err == nil && parsed > 0 && parsed < 500 {
			n = parsed
		}
	}
	data, err := os.ReadFile(run.StderrPath)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No stderr log available.")
		return nil
	}
	lines := strings.Split(a.redactor.Redact(string(data)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, strings.Join(lines, "\n"))
	return nil
}

func (a *App) cmdArtifacts(ctx context.Context, msg TelegramMessage) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	run, err := LatestRunForContext(ctx, a.db, c.ID)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No runs yet.")
		return nil
	}
	entries, err := os.ReadDir(run.ArtifactDir)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("Artifacts:\n")
	for _, e := range entries {
		b.WriteString("- " + filepath.Join(run.ArtifactDir, e.Name()) + "\n")
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, b.String())
	return nil
}

func (a *App) cmdSendFile(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	relPath := strings.TrimSpace(args)
	if relPath == "" {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /sendfile <workspace-relative-path>")
		return nil
	}
	path, err := ResolveWorkspaceFile(a.cfg, c.ID, relPath, a.cfg.MaxAttachmentBytes)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Cannot send file: "+err.Error())
		return nil
	}
	sent, err := a.tg.SendDocument(ctx, msg.Chat.ID, msg.MessageThreadID, path, filepath.Base(path))
	if err != nil {
		return err
	}
	_, _ = StoreMessage(ctx, a.db, StoredMessage{
		ChatID:            sent.Chat.ID,
		TopicID:           sent.MessageThreadID,
		TelegramMessageID: sent.MessageID,
		SenderID:          sent.From.ID,
		SenderName:        "servitor",
		Caption:           sent.Caption,
		IsBot:             true,
		IsAdmin:           true,
	})
	return nil
}

func (a *App) cmdLoop(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	cronExpr, prompt, ok := splitCronAndPrompt(args)
	if !ok {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /loop <5-field-cron> <prompt>")
		return nil
	}
	next, err := NextCronTime(cronExpr, time.Now().In(a.cfg.ServiceTimezone), a.cfg.ServiceTimezone)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Invalid cron: "+err.Error())
		return nil
	}
	id := NewID("sch")
	if err := CreateSchedule(ctx, a.db, Schedule{ID: id, ContextID: c.ID, CronExpr: cronExpr, Prompt: prompt, Enabled: true, NextRunAt: next}); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Created schedule %s. Next run: %s", id, next.Format(time.RFC3339)))
	return nil
}

func (a *App) cmdLoops(ctx context.Context, msg TelegramMessage) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	schedules, err := ListSchedules(ctx, a.db, c.ID)
	if err != nil {
		return err
	}
	if len(schedules) == 0 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No schedules.")
		return nil
	}
	var b strings.Builder
	for _, s := range schedules {
		b.WriteString(fmt.Sprintf("%s enabled=%t cron=%q next=%s prompt=%q\n", s.ID, s.Enabled, s.CronExpr, s.NextRunAt.Format(time.RFC3339), s.Prompt))
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, b.String())
	return nil
}

func (a *App) cmdUnloop(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	id := strings.TrimSpace(args)
	if id == "" {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /unloop <id>")
		return nil
	}
	if err := DeleteSchedule(ctx, a.db, id, c.ID); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Deleted schedule "+id+".")
	return nil
}

func helpText() string {
	return strings.Join([]string{
		"/help",
		"/whoami",
		"/newctx scratch",
		"/newctx repo <https-url>",
		"/bind <context_id>",
		"/detach",
		"/topicinfo",
		"/explainctx",
		"/run <prompt>",
		"/resume <prompt>",
		"/archive",
		"/tail [n]",
		"/artifacts",
		"/sendfile <workspace-relative-path>",
		"/loop <5-field-cron> <prompt>",
		"/loops",
		"/unloop <id>",
	}, "\n")
}

func splitCronAndPrompt(args string) (string, string, bool) {
	fields := strings.Fields(args)
	if len(fields) < 6 {
		return "", "", false
	}
	return strings.Join(fields[:5], " "), strings.Join(fields[5:], " "), true
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
