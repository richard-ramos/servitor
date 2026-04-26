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

type commandHelp struct {
	Command     string
	Description string
	Usage       []string
}

var servitorCommands = []commandHelp{
	{Command: "help", Description: "List commands", Usage: []string{"/help"}},
	{Command: "whoami", Description: "Show your Telegram user ID", Usage: []string{"/whoami"}},
	{Command: "newctx", Description: "Create and bind a context", Usage: []string{"/newctx scratch", "/newctx repo <https-url>"}},
	{Command: "bind", Description: "Bind this topic to a context", Usage: []string{"/bind <context_id>"}},
	{Command: "detach", Description: "Detach this topic from its context", Usage: []string{"/detach"}},
	{Command: "topicinfo", Description: "Show this topic's context binding", Usage: []string{"/topicinfo"}},
	{Command: "explainctx", Description: "Show context details", Usage: []string{"/explainctx"}},
	{Command: "contexts", Description: "List contexts", Usage: []string{"/contexts"}},
	{Command: "switch", Description: "Switch this topic to a context", Usage: []string{"/switch <context_id_or_name>"}},
	{Command: "renamectx", Description: "Rename the bound context", Usage: []string{"/renamectx <name>"}},
	{Command: "run", Description: "Run a prompt in the bound context", Usage: []string{"/run <prompt>"}},
	{Command: "resume", Description: "Resume the bound context session", Usage: []string{"/resume <prompt>"}},
	{Command: "status", Description: "Show run and queue status", Usage: []string{"/status"}},
	{Command: "cancel", Description: "Cancel current or pending work", Usage: []string{"/cancel [queue_id]"}},
	{Command: "retry", Description: "Retry a failed queue item", Usage: []string{"/retry [queue_id]"}},
	{Command: "archive", Description: "Archive the bound context", Usage: []string{"/archive"}},
	{Command: "tail", Description: "Show recent run output", Usage: []string{"/tail [n]"}},
	{Command: "artifacts", Description: "List workspace artifacts", Usage: []string{"/artifacts"}},
	{Command: "sendfile", Description: "Send a workspace file to Telegram", Usage: []string{"/sendfile <workspace-relative-path>"}},
	{Command: "task", Description: "Manage scheduled tasks", Usage: []string{
		"/task add cron <5-field-cron> <prompt>",
		"/task add interval <duration> <prompt>",
		"/task add once <time> <prompt>",
		"/task add-script <cron|interval|once> <spec> <workspace-relative-script> [prompt]",
		"/task list",
		"/task history <id>",
		"/task pause <id>",
		"/task resume <id>",
		"/task cancel <id>",
		"/task update <id> <prompt|cron|interval|once|script> <value>",
	}},
	{Command: "usage", Description: "Show token usage", Usage: []string{"/usage [run_id]"}},
	{Command: "reasoning", Description: "Set or show reasoning effort", Usage: []string{"/reasoning [low|medium|high|xhigh]"}},
	{Command: "skills", Description: "List available skills", Usage: []string{"/skills"}},
	{Command: "useskill", Description: "Enable a skill for this context", Usage: []string{"/useskill <name>"}},
	{Command: "unuseskill", Description: "Disable a skill for this context", Usage: []string{"/unuseskill <name>"}},
	{Command: "ctxskills", Description: "List skills enabled for this context", Usage: []string{"/ctxskills"}},
	{Command: "agents", Description: "Toggle AGENTS.md for this context", Usage: []string{"/agents on|off"}},
	{Command: "loop", Description: "Create a cron loop task", Usage: []string{"/loop <5-field-cron> <prompt>"}},
	{Command: "loops", Description: "List loop tasks", Usage: []string{"/loops"}},
	{Command: "unloop", Description: "Cancel a loop task", Usage: []string{"/unloop <id>"}},
	{Command: "synccommands", Description: "Refresh Telegram slash commands", Usage: []string{"/synccommands"}},
	{Command: "showcommands", Description: "Show Telegram slash commands", Usage: []string{"/showcommands"}},
}

func telegramBotCommands() []TelegramBotCommand {
	commands := make([]TelegramBotCommand, 0, len(servitorCommands))
	for _, cmd := range servitorCommands {
		commands = append(commands, TelegramBotCommand{
			Command:     cmd.Command,
			Description: cmd.Description,
		})
	}
	return commands
}

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
	a.reactQueued(ctx, msg)
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
	case "/contexts":
		return a.cmdContexts(ctx, msg)
	case "/switch":
		return a.cmdSwitch(ctx, msg, args)
	case "/renamectx":
		return a.cmdRenameContext(ctx, msg, args)
	case "/run":
		return a.cmdRun(ctx, msg, messageID, args, false)
	case "/resume":
		return a.cmdRun(ctx, msg, messageID, args, true)
	case "/status":
		return a.cmdStatus(ctx, msg)
	case "/cancel":
		return a.cmdCancel(ctx, msg, args)
	case "/retry":
		return a.cmdRetry(ctx, msg, args)
	case "/archive":
		return a.cmdArchive(ctx, msg)
	case "/tail":
		return a.cmdTail(ctx, msg, args)
	case "/artifacts":
		return a.cmdArtifacts(ctx, msg)
	case "/sendfile":
		return a.cmdSendFile(ctx, msg, args)
	case "/task":
		return a.cmdTask(ctx, msg, args)
	case "/usage":
		return a.cmdUsage(ctx, msg, args)
	case "/reasoning":
		return a.cmdReasoning(ctx, msg, args)
	case "/skills":
		return a.cmdSkills(ctx, msg)
	case "/useskill":
		return a.cmdUseSkill(ctx, msg, args, true)
	case "/unuseskill":
		return a.cmdUseSkill(ctx, msg, args, false)
	case "/ctxskills":
		return a.cmdContextSkills(ctx, msg)
	case "/agents":
		return a.cmdAgents(ctx, msg, args)
	case "/loop":
		return a.cmdLoop(ctx, msg, args)
	case "/loops":
		return a.cmdTaskList(ctx, msg)
	case "/unloop":
		return a.cmdTaskCancel(ctx, msg, strings.TrimSpace(args))
	case "/synccommands":
		return a.cmdSyncCommands(ctx, msg)
	case "/showcommands":
		return a.cmdShowCommands(ctx, msg)
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
	c := Context{ID: id, Kind: kind, State: ContextStateActive, RepoURL: repoURL, WorkspaceDir: ContextWorkspaceDir(a.cfg, id), ReasoningEffort: a.cfg.DefaultReasoningEffort}
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
	if err := writeCodexConfig(a.cfg, id, a.cfg.DefaultReasoningEffort); err != nil {
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

func writeCodexConfig(cfg Config, id string, reasoningEffort string) error {
	dir := ContextCodexDir(cfg, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	reasoningEffort = effectiveReasoningEffort(cfg, reasoningEffort)
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
`, cfg.DefaultCodexModel, reasoningEffort, cfg.ContainerProxyChatGPTBaseURL())
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
`, cfg.DefaultCodexModel, reasoningEffort, cfg.ContainerProxyBaseURL())
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
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Context: %s\nKind: %s\nState: %s\nReasoning effort: %s\nWorkspace: %s", contextLabel(c), c.Kind, c.State, c.ReasoningEffort, c.WorkspaceDir))
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
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Context: %s\nKind: %s\nState: %s\nRepo: %s\nReasoning effort: %s\nWorkspace: %s\nSession: %s\nLatest run: %s", contextLabel(c), c.Kind, c.State, c.RepoURL, c.ReasoningEffort, c.WorkspaceDir, emptyDash(c.CodexSession), latest))
	return nil
}

func (a *App) cmdContexts(ctx context.Context, msg TelegramMessage) error {
	contexts, err := ListContexts(ctx, a.db)
	if err != nil {
		return err
	}
	if len(contexts) == 0 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No contexts.")
		return nil
	}
	currentID := ""
	if current, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID); err == nil {
		currentID = current.ID
	}
	var b strings.Builder
	for _, c := range contexts {
		marker := " "
		if c.ID == currentID {
			marker = "*"
		}
		name := ""
		if c.DisplayName != "" {
			name = " name=" + strconv.Quote(c.DisplayName)
		}
		b.WriteString(fmt.Sprintf("%s %s%s state=%s kind=%s updated=%s\n", marker, c.ID, name, c.State, c.Kind, formatTimeShort(c.UpdatedAt)))
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, strings.TrimRight(b.String(), "\n"))
	return nil
}

func (a *App) cmdSwitch(ctx context.Context, msg TelegramMessage, args string) error {
	ref := strings.TrimSpace(args)
	if ref == "" {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /switch <context_id_or_name>")
		return nil
	}
	c, err := ResolveContext(ctx, a.db, ref)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Context not found.")
			return nil
		}
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, err.Error())
		return nil
	}
	if c.State == ContextStateArchived {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Context is archived.")
		return nil
	}
	if err := BindTopic(ctx, a.db, msg.Chat.ID, msg.MessageThreadID, c.ID); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Switched topic to "+contextLabel(c)+".")
	return nil
}

func (a *App) cmdRenameContext(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	name, err := normalizeContextDisplayName(args)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, err.Error())
		return nil
	}
	if err := UpdateContextDisplayName(ctx, a.db, c.ID, name); err != nil {
		return err
	}
	if name == "" {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Cleared context name for "+c.ID+".")
		return nil
	}
	c.DisplayName = name
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Renamed context to "+contextLabel(c)+".")
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
	a.reactQueued(ctx, msg)
	return nil
}

func (a *App) cmdStatus(ctx context.Context, msg TelegramMessage) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	counts, err := QueueCountsForContext(ctx, a.db, c.ID)
	if err != nil {
		return err
	}
	items, err := QueueItemsForContext(ctx, a.db, c.ID, 5)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Context: %s\nState: %s\n", contextLabel(c), c.State))
	b.WriteString(fmt.Sprintf("Queue: pending=%d running=%d done=%d failed=%d\n", counts[QueueStatusPending], counts[QueueStatusRunning], counts[QueueStatusDone], counts[QueueStatusFailed]))
	if run, ok := a.activeRunForContext(c.ID); ok {
		b.WriteString(fmt.Sprintf("Active run: #%d queue=%d stage=%s elapsed=%s\n", run.RunID, run.QueueID, emptyDash(run.Stage), time.Since(run.StartedAt).Round(time.Second)))
	} else if latest, err := LatestRunForContext(ctx, a.db, c.ID); err == nil {
		finished := "running"
		if latest.FinishedAt != nil {
			finished = formatTimeShort(*latest.FinishedAt)
		}
		b.WriteString(fmt.Sprintf("Latest run: #%d status=%s exit=%d finished=%s\n", latest.ID, latest.Status, latest.ExitCode, finished))
	}
	if len(items) > 0 {
		b.WriteString("Recent queue:\n")
		for _, item := range items {
			runPart := ""
			if item.CurrentRunID != 0 {
				runPart = fmt.Sprintf(" run=%d", item.CurrentRunID)
			}
			b.WriteString(fmt.Sprintf("#%d %s%s attempts=%d prompt=%q\n", item.ID, queueStatusLabel(item), runPart, item.Attempts, truncateOneLine(item.Prompt, 72)))
		}
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, strings.TrimRight(b.String(), "\n"))
	return nil
}

func (a *App) cmdCancel(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	arg := strings.TrimSpace(args)
	if arg != "" {
		queueID, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /cancel [queue_id]")
			return nil
		}
		item, err := GetQueueItemForContext(ctx, a.db, c.ID, queueID)
		if err != nil {
			if isNoRows(err) {
				_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Queue item not found for this context.")
				return nil
			}
			return err
		}
		switch item.Status {
		case QueueStatusPending:
			ok, err := CancelPendingQueueItem(ctx, a.db, c.ID, item.ID)
			if err != nil {
				return err
			}
			if ok {
				_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Cancelled queue item #%d.", item.ID))
				return nil
			}
		case QueueStatusRunning:
			if run, ok := a.activeRunForContext(c.ID); ok && (item.CurrentRunID == 0 || item.CurrentRunID == run.RunID) {
				run.Cancel()
				_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Cancel requested for run #%d.", run.RunID))
				return nil
			}
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Queue item is running, but no local cancellation handle is available.")
			return nil
		default:
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Queue item #%d is already %s.", item.ID, queueStatusLabel(item)))
			return nil
		}
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Queue item changed before it could be cancelled.")
		return nil
	}
	run, cancelledRun := a.cancelActiveRunForContext(c.ID)
	pending, err := CancelPendingQueueForContext(ctx, a.db, c.ID)
	if err != nil {
		return err
	}
	if !cancelledRun && pending == 0 {
		counts, err := QueueCountsForContext(ctx, a.db, c.ID)
		if err != nil {
			return err
		}
		if counts[QueueStatusRunning] > 0 {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "A queue item is marked running, but no local cancellation handle is available.")
			return nil
		}
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No running or pending work for "+contextLabel(c)+".")
		return nil
	}
	var parts []string
	if cancelledRun {
		parts = append(parts, fmt.Sprintf("cancel requested for run #%d", run.RunID))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("cancelled %d pending queue item(s)", pending))
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, strings.Join(parts, "; ")+".")
	return nil
}

func (a *App) cmdRetry(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	var item QueueItem
	arg := strings.TrimSpace(args)
	if arg == "" {
		item, err = LatestFailedQueueForContext(ctx, a.db, c.ID)
		if err != nil {
			if isNoRows(err) {
				_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No failed queue item to retry.")
				return nil
			}
			return err
		}
	} else {
		queueID, parseErr := strconv.ParseInt(arg, 10, 64)
		if parseErr != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /retry [queue_id]")
			return nil
		}
		item, err = GetQueueItemForContext(ctx, a.db, c.ID, queueID)
		if err != nil {
			if isNoRows(err) {
				_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Queue item not found for this context.")
				return nil
			}
			return err
		}
	}
	if item.Status != QueueStatusFailed {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Queue item #%d is %s, not failed.", item.ID, queueStatusLabel(item)))
		return nil
	}
	newID, err := RetryQueueItem(ctx, a.db, item)
	if err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Retried queue item #%d as #%d.", item.ID, newID))
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
	return a.cmdTaskAddCron(ctx, msg, args)
}

func (a *App) cmdTask(ctx context.Context, msg TelegramMessage, args string) error {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task add|add-script|list|history|pause|resume|cancel|update ...")
		return nil
	}
	switch fields[0] {
	case "add":
		return a.cmdTaskAdd(ctx, msg, strings.TrimSpace(strings.TrimPrefix(args, "add")))
	case "add-script":
		return a.cmdTaskAddScript(ctx, msg, strings.TrimSpace(strings.TrimPrefix(args, "add-script")))
	case "list":
		return a.cmdTaskList(ctx, msg)
	case "history":
		if len(fields) < 2 {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task history <id>")
			return nil
		}
		return a.cmdTaskHistory(ctx, msg, fields[1])
	case "pause":
		return a.cmdTaskSetStatus(ctx, msg, fields, ScheduleStatusPaused)
	case "resume":
		return a.cmdTaskResume(ctx, msg, fields)
	case "cancel":
		if len(fields) < 2 {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task cancel <id>")
			return nil
		}
		return a.cmdTaskCancel(ctx, msg, fields[1])
	case "update":
		return a.cmdTaskUpdate(ctx, msg, strings.TrimSpace(strings.TrimPrefix(args, "update")))
	default:
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task add|add-script|list|history|pause|resume|cancel|update ...")
		return nil
	}
}

func (a *App) cmdTaskAdd(ctx context.Context, msg TelegramMessage, args string) error {
	fields := strings.Fields(args)
	if len(fields) < 3 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task add cron|interval|once <spec> <prompt>")
		return nil
	}
	kind := fields[0]
	switch kind {
	case ScheduleKindCron:
		return a.cmdTaskAddCron(ctx, msg, strings.Join(fields[1:], " "))
	case ScheduleKindInterval:
		spec := fields[1]
		prompt := strings.Join(fields[2:], " ")
		return a.createTask(ctx, msg, kind, spec, "", prompt)
	case ScheduleKindOnce:
		spec := fields[1]
		prompt := strings.Join(fields[2:], " ")
		return a.createTask(ctx, msg, kind, spec, "", prompt)
	default:
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Task kind must be cron, interval, or once.")
		return nil
	}
}

func (a *App) cmdTaskAddCron(ctx context.Context, msg TelegramMessage, args string) error {
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
	if err := CreateSchedule(ctx, a.db, Schedule{ID: id, ContextID: c.ID, Kind: ScheduleKindCron, Status: ScheduleStatusActive, CronExpr: cronExpr, Prompt: prompt, Enabled: true, NextRunAt: next}); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Created schedule %s. Next run: %s", id, next.Format(time.RFC3339)))
	return nil
}

func (a *App) cmdTaskAddScript(ctx context.Context, msg TelegramMessage, args string) error {
	fields := strings.Fields(args)
	if len(fields) < 3 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task add-script <cron|interval|once> <spec> <workspace-relative-script> [prompt]")
		return nil
	}
	kind := fields[0]
	switch kind {
	case ScheduleKindCron:
		if len(fields) < 7 {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task add-script cron <5-field-cron> <script> [prompt]")
			return nil
		}
		spec := strings.Join(fields[1:6], " ")
		return a.createTask(ctx, msg, kind, spec, fields[6], strings.Join(fields[7:], " "))
	case ScheduleKindInterval, ScheduleKindOnce:
		return a.createTask(ctx, msg, kind, fields[1], fields[2], strings.Join(fields[3:], " "))
	default:
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Task kind must be cron, interval, or once.")
		return nil
	}
}

func (a *App) createTask(ctx context.Context, msg TelegramMessage, kind, spec, scriptPath, prompt string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	if scriptPath != "" {
		if _, err := ResolveWorkspaceFile(a.cfg, c.ID, scriptPath, 0); err != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Invalid script path: "+err.Error())
			return nil
		}
	}
	s, err := InitialNextRun(kind, spec, a.cfg.ServiceTimezone)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Invalid schedule: "+err.Error())
		return nil
	}
	s.ID = NewID("sch")
	s.ContextID = c.ID
	s.ScriptPath = scriptPath
	s.Prompt = prompt
	if err := CreateSchedule(ctx, a.db, s); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Created schedule %s. Next run: %s", s.ID, s.NextRunAt.Format(time.RFC3339)))
	return nil
}

func (a *App) cmdTaskList(ctx context.Context, msg TelegramMessage) error {
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
		spec := s.CronExpr
		if s.Kind == ScheduleKindInterval {
			spec = fmt.Sprintf("%ds", s.IntervalSeconds)
		}
		if s.Kind == ScheduleKindOnce && s.RunAt != nil {
			spec = s.RunAt.Format(time.RFC3339)
		}
		b.WriteString(fmt.Sprintf("%s status=%s kind=%s spec=%q next=%s script=%q prompt=%q\n", s.ID, s.Status, s.Kind, spec, s.NextRunAt.Format(time.RFC3339), s.ScriptPath, s.Prompt))
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, b.String())
	return nil
}

func (a *App) cmdTaskCancel(ctx context.Context, msg TelegramMessage, id string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	if id == "" {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task cancel <id>")
		return nil
	}
	if err := DeleteSchedule(ctx, a.db, id, c.ID); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Cancelled schedule "+id+".")
	return nil
}

func (a *App) cmdTaskSetStatus(ctx context.Context, msg TelegramMessage, fields []string, status string) error {
	if len(fields) < 2 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task pause <id>")
		return nil
	}
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	if err := UpdateScheduleStatus(ctx, a.db, fields[1], c.ID, status); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Schedule %s is now %s.", fields[1], status))
	return nil
}

func (a *App) cmdTaskResume(ctx context.Context, msg TelegramMessage, fields []string) error {
	if len(fields) < 2 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task resume <id>")
		return nil
	}
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	s, err := GetSchedule(ctx, a.db, fields[1], c.ID)
	if err != nil {
		return err
	}
	next, err := recomputeNextRun(s, a.cfg.ServiceTimezone)
	if err != nil {
		return err
	}
	s.Status = ScheduleStatusActive
	s.NextRunAt = next
	if err := UpdateSchedule(ctx, a.db, s); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Schedule %s resumed. Next run: %s", s.ID, s.NextRunAt.Format(time.RFC3339)))
	return nil
}

func (a *App) cmdTaskHistory(ctx context.Context, msg TelegramMessage, id string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	history, err := ScheduleHistory(ctx, a.db, id, c.ID)
	if err != nil {
		return err
	}
	if len(history) == 0 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No runs for schedule.")
		return nil
	}
	var b strings.Builder
	for _, h := range history {
		finished := "-"
		if h.FinishedAt != nil {
			finished = h.FinishedAt.Format(time.RFC3339)
		}
		b.WriteString(fmt.Sprintf("queue=%d run=%d status=%s exit=%d created=%s finished=%s\n", h.QueueID, h.RunID, h.Status, h.ExitCode, h.CreatedAt.Format(time.RFC3339), finished))
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, b.String())
	return nil
}

func (a *App) cmdTaskUpdate(ctx context.Context, msg TelegramMessage, args string) error {
	fields := strings.Fields(args)
	if len(fields) < 3 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task update <id> <prompt|cron|interval|once|script> <value>")
		return nil
	}
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	s, err := GetSchedule(ctx, a.db, fields[0], c.ID)
	if err != nil {
		return err
	}
	switch fields[1] {
	case "prompt":
		s.Prompt = strings.Join(fields[2:], " ")
	case "script":
		if _, err := ResolveWorkspaceFile(a.cfg, c.ID, fields[2], 0); err != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Invalid script path: "+err.Error())
			return nil
		}
		s.ScriptPath = fields[2]
	case ScheduleKindCron, ScheduleKindInterval, ScheduleKindOnce:
		updated, err := InitialNextRun(fields[1], strings.Join(fields[2:], " "), a.cfg.ServiceTimezone)
		if fields[1] == ScheduleKindCron {
			cronExpr, _, ok := splitCronAndPrompt(strings.Join(fields[2:], " ") + " x")
			if !ok {
				_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /task update <id> cron <5-field-cron>")
				return nil
			}
			updated, err = InitialNextRun(fields[1], cronExpr, a.cfg.ServiceTimezone)
		}
		if err != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Invalid schedule: "+err.Error())
			return nil
		}
		s.Kind = updated.Kind
		s.CronExpr = updated.CronExpr
		s.IntervalSeconds = updated.IntervalSeconds
		s.RunAt = updated.RunAt
		s.NextRunAt = updated.NextRunAt
		s.Status = ScheduleStatusActive
	default:
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Update field must be prompt, cron, interval, once, or script.")
		return nil
	}
	if err := UpdateSchedule(ctx, a.db, s); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Updated schedule "+s.ID+".")
	return nil
}

func (a *App) cmdUsage(ctx context.Context, msg TelegramMessage, args string) error {
	if strings.TrimSpace(args) != "" {
		runID, err := strconv.ParseInt(strings.TrimSpace(args), 10, 64)
		if err != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /usage [run_id]")
			return nil
		}
		u, err := UsageForRun(ctx, a.db, runID)
		if err != nil {
			if isNoRows(err) {
				_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No usage record for run.")
				return nil
			}
			return err
		}
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Run #%d model=%s input=%d output=%d total=%d", u.RunID, emptyDash(u.Model), u.InputTokens, u.OutputTokens, u.TotalTokens))
		return nil
	}
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	total, err := UsageTotalsForContext(ctx, a.db, c.ID)
	if err != nil {
		return err
	}
	recent, err := RecentUsageForContext(ctx, a.db, c.ID, 5)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Context %s usage: input=%d output=%d total=%d\n", c.ID, total.InputTokens, total.OutputTokens, total.TotalTokens))
	for _, u := range recent {
		b.WriteString(fmt.Sprintf("run=%d model=%s input=%d output=%d total=%d\n", u.RunID, emptyDash(u.Model), u.InputTokens, u.OutputTokens, u.TotalTokens))
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, b.String())
	return nil
}

func (a *App) cmdReasoning(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	if c.State == ContextStateArchived {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Context is archived.")
		return nil
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		effort := c.ReasoningEffort
		if effort == "" {
			effort = a.cfg.DefaultReasoningEffort
		}
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("model_reasoning_effort: %s\nUsage: /reasoning [low|medium|high|xhigh]", effort))
		return nil
	}
	if len(fields) != 1 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Usage: /reasoning [low|medium|high|xhigh]")
		return nil
	}
	effort, ok := normalizeReasoningEffort(fields[0])
	if !ok {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Invalid reasoning effort. Use one of: %s", reasoningEffortChoices()))
		return nil
	}
	if err := UpdateContextReasoningEffort(ctx, a.db, c.ID, effort); err != nil {
		return err
	}
	if err := writeCodexConfig(a.cfg, c.ID, effort); err != nil {
		return err
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("model_reasoning_effort set to %s for context %s.", effort, c.ID))
	return nil
}

func (a *App) cmdSkills(ctx context.Context, msg TelegramMessage) error {
	skills, err := AvailableSkills(a.cfg)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Cannot list skills: "+err.Error())
		return nil
	}
	if len(skills) == 0 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No skills found.")
		return nil
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, strings.Join(skills, "\n"))
	return nil
}

func (a *App) cmdUseSkill(ctx context.Context, msg TelegramMessage, args string, enabled bool) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	name := strings.TrimSpace(args)
	if !IsSafeSkillName(name) {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Invalid skill name.")
		return nil
	}
	if enabled {
		if err := ValidateSkillExists(a.cfg, name); err != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Cannot enable skill: "+err.Error())
			return nil
		}
	}
	if err := SetContextSkill(ctx, a.db, c.ID, name, enabled); err != nil {
		return err
	}
	if enabled {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Enabled skill "+name+".")
	} else {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Disabled skill "+name+".")
	}
	return nil
}

func (a *App) cmdContextSkills(ctx context.Context, msg TelegramMessage) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	skills, err := ListContextSkills(ctx, a.db, c.ID)
	if err != nil {
		return err
	}
	if len(skills) == 0 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No skills enabled for this context.")
		return nil
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, strings.Join(skills, "\n"))
	return nil
}

func (a *App) cmdAgents(ctx context.Context, msg TelegramMessage, args string) error {
	c, err := GetBoundContext(ctx, a.db, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		if isNoRows(err) {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No context bound.")
			return nil
		}
		return err
	}
	switch strings.TrimSpace(args) {
	case "on":
		if err := ValidateAgentsFile(a.cfg); err != nil {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Cannot enable AGENTS.md: "+err.Error())
			return nil
		}
		if err := SetContextAgentsEnabled(ctx, a.db, c.ID, true); err != nil {
			return err
		}
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "AGENTS.md enabled for this context.")
	case "off":
		if err := SetContextAgentsEnabled(ctx, a.db, c.ID, false); err != nil {
			return err
		}
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "AGENTS.md disabled for this context.")
	default:
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("AGENTS.md enabled: %t\nUsage: /agents on|off", c.AgentsEnabled))
	}
	return nil
}

func (a *App) cmdSyncCommands(ctx context.Context, msg TelegramMessage) error {
	if err := a.SyncTelegramCommands(ctx); err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Failed to sync Telegram commands: "+err.Error())
		return nil
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Synced %d Telegram commands.", len(servitorCommands)))
	return nil
}

func (a *App) cmdShowCommands(ctx context.Context, msg TelegramMessage) error {
	commands, err := a.tg.GetMyCommands(ctx)
	if err != nil {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Failed to fetch Telegram commands: "+err.Error())
		return nil
	}
	if len(commands) == 0 {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "No Telegram commands are registered.")
		return nil
	}
	_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, formatBotCommands(commands))
	return nil
}

func (a *App) SyncTelegramCommands(ctx context.Context) error {
	return a.tg.SetMyCommands(ctx, telegramBotCommands())
}

func formatBotCommands(commands []TelegramBotCommand) string {
	lines := make([]string, 0, len(commands))
	for _, cmd := range commands {
		if cmd.Description == "" {
			lines = append(lines, "/"+cmd.Command)
			continue
		}
		lines = append(lines, fmt.Sprintf("/%s - %s", cmd.Command, cmd.Description))
	}
	return strings.Join(lines, "\n")
}

func helpText() string {
	var lines []string
	for _, cmd := range servitorCommands {
		lines = append(lines, cmd.Usage...)
	}
	return strings.Join(lines, "\n")
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

func contextLabel(c Context) string {
	if c.DisplayName == "" {
		return c.ID
	}
	return fmt.Sprintf("%s (%s)", c.ID, c.DisplayName)
}

func normalizeContextDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("Usage: /renamectx <name>")
	}
	if name == "-" {
		return "", nil
	}
	if len(name) > 80 {
		return "", fmt.Errorf("Context name must be 80 bytes or less.")
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return "", fmt.Errorf("Context name cannot contain control characters.")
		}
	}
	return name, nil
}

func queueStatusLabel(q QueueItem) string {
	if q.Status == QueueStatusFailed && q.FailureClass != "" {
		return q.Status + "/" + q.FailureClass
	}
	return q.Status
}

func truncateOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func formatTimeShort(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}
