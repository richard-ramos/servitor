# Operations

Use `scripts/install.sh` to build the binary and install a user systemd unit.

Use `scripts/doctor.sh` to check the local Go/Docker/systemd environment and print recent service logs.

Important environment variables:

- `ADMIN_USER_IDS`: comma-separated Telegram user IDs allowed to control the bot.
- `CODEX_AUTH_MODE`: `chatgpt` by default, or `api_key` as an explicit fallback.
- `CODEX_HOST_AUTH_DIR`: host Codex auth/cache directory, usually `~/.codex`.
- `DEFAULT_REASONING_EFFORT`: default `model_reasoning_effort` for new contexts, `xhigh` unless overridden.
- `SKILLS_DIR`: host skill registry copied into opted-in contexts, default `$CODEX_HOST_AUTH_DIR/skills`.
- `AGENTS_FILE`: host AGENTS.md copied into opted-in contexts, default `$CODEX_HOST_AUTH_DIR/AGENTS.md`.
- `OPENAI_PROXY_BIND_HOST`: explicit Docker-reachable bind address for the credential proxy when auto-detection is not enough.
- `MAX_CONCURRENT_CONTAINERS`: global Docker concurrency.
- `MAX_RUN_SECONDS`: timeout for a single Codex run.
- `MAX_OUTPUT_BYTES`: captured stdout/stderr limit.
- `MAX_ATTACHMENT_BYTES`: Telegram attachment size cap.
- `AGENT_MESSAGES_ENABLED`: allow controlled same-topic updates from Codex through `servitor-send`.
- `AGENT_MESSAGE_MAX_PER_RUN`: maximum accepted agent messages per run.
- `AGENT_MESSAGE_MAX_CHARS`: maximum characters per accepted agent message.
- `PROGRESS_UPDATES`: host-generated run lifecycle messages, enabled by default.
- `PROGRESS_INTERVAL_SECONDS`: interval for “still running” updates.

Run logs exposed through `/tail` are sanitized with Servitor's redactor before being posted to Telegram.

For the default OAuth-style auth path, run `codex login --device-auth` on the host before starting Servitor. The service checks `codex login status` and reads the host-owned access token for proxy injection without mounting the auth cache into Docker containers. Containers use a generated Codex custom provider named `servitor` and send Responses traffic to the host proxy with a placeholder token.

Agent message and file-send requests are recorded in each run directory as `agent_messages.jsonl`; host validation decisions are recorded in `agent_messages_audit.jsonl`. Codex can request a text update with `servitor-send "message"` or request a workspace file upload with `servitor-send-file hello.txt "caption"`.

Interactive agent actions are recorded in `agent_actions.jsonl` with host decisions in `agent_actions_audit.jsonl`. Codex can request same-topic questions, edits, reactions, approved schedule changes, and same-chat context messages with `servitor-action`.

Use `/task` for cron, interval, one-shot, pause/resume/cancel/update, script-backed tasks, and run history. `/loop`, `/loops`, and `/unloop` remain compatibility aliases for cron schedules.

Use `/reasoning [low|medium|high|xhigh]` to view or set the bound context's Codex `model_reasoning_effort`. The setting is persisted on the context and written into its `.codex/config.toml` before runs.

Use `/status`, `/cancel [queue_id]`, and `/retry [queue_id]` to inspect, stop, or requeue work in the bound context. Cancelled queue items are stored as failed with `failure_class=cancelled`.

Use `/contexts`, `/switch <context_id_or_name>`, and `/renamectx <name>` to browse contexts, bind a topic to another context, and assign a human-readable name.

Use `/skills`, `/useskill`, `/unuseskill`, `/ctxskills`, and `/agents on|off` to control context opt-in skill and AGENTS.md injection. Servitor copies selected files into the context `.codex`; it does not mount host Codex directories into containers.

Use `/sendfile <workspace-relative-path>` to send a file from the bound context workspace as a Telegram document. For example, `/sendfile hello.txt` sends `$DATA_DIR/contexts/<id>/workspace/hello.txt`. Absolute paths, `..`, symlink escapes, directories, and files larger than `MAX_ATTACHMENT_BYTES` are rejected.
