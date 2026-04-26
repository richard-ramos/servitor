# Servitor

Servitor is a Codex control plane for Telegram forum topics. A trusted Telegram admin binds a topic to a durable local context, then topic messages are queued as Codex runs inside isolated ephemeral Docker containers.

## v1 Scope

- Telegram Bot API long polling.
- Admin-only commands and prompts.
- Scratch and public HTTPS repository contexts.
- Recent topic history and reply context in prompt assembly.
- Attachment download into the context workspace with path references.
- Local Docker execution with one fresh container per run.
- Host-side Codex/OpenAI credential proxy so real OAuth/API credentials never enter containers.
- Cron, interval, and one-shot schedules through `/task`; `/loop`, `/loops`, and `/unloop` remain compatibility aliases.
- Usage accounting parsed best-effort from Codex JSONL output.
- Context opt-in skill injection and AGENTS.md support.
- Controlled agent-to-Telegram updates through host-validated `servitor-send`, `servitor-send-file`, and `servitor-action` helpers.
- Interactive outbound actions through Telegram inline buttons, edits, reactions, and callback continuations.

Deferred: private repo credentials, host directory contexts, remote workers, swarms, MCP, and multichannel support.

## Setup

1. Copy `.env.example` to `.env`.
2. Fill `TELEGRAM_BOT_TOKEN` and `ADMIN_USER_IDS`.
3. Log in to Codex on the host:

```sh
codex login --device-auth
codex login status
```

4. Run:

```sh
go build -o servitor .
./servitor
```

The default `CODEX_AUTH_MODE=chatgpt` uses the host Codex login cache at `CODEX_HOST_AUTH_DIR=~/.codex`. Servitor generates each context's container-side Codex config with a custom `servitor` model provider, `requires_openai_auth = true`, and a proxy URL; the container receives only a placeholder JWT-shaped token. The service builds `Dockerfile.agent` into `servitor-agent:latest` on startup unless `SERVITOR_SKIP_DOCKER_BUILD=true`.

If `OPENAI_PROXY_HOST=127.0.0.1`, Servitor binds to the detected Docker bridge address. Set `OPENAI_PROXY_BIND_HOST` explicitly if Docker uses a non-standard bridge. The proxy requires a per-process placeholder bearer token before injecting real host credentials.

API-key fallback is available by setting:

```env
CODEX_AUTH_MODE=api_key
OPENAI_API_KEY=...
```

## Telegram Commands

- `/newctx scratch`
- `/newctx repo <https-url>`
- `/bind <context_id>`
- `/detach`
- `/topicinfo`
- `/explainctx`
- `/contexts`
- `/switch <context_id_or_name>`
- `/renamectx <name>`
- `/run <prompt>`
- `/resume <prompt>`
- `/status`
- `/cancel [queue_id]`
- `/retry [queue_id]`
- `/archive`
- `/tail [n]`
- `/artifacts`
- `/sendfile <workspace-relative-path>`
- `/task add cron <5-field-cron> <prompt>`
- `/task add interval <duration> <prompt>`
- `/task add once <time> <prompt>`
- `/task add-script <cron|interval|once> <spec> <workspace-relative-script> [prompt]`
- `/task list`
- `/task history <id>`
- `/task pause <id>`
- `/task resume <id>`
- `/task cancel <id>`
- `/task update <id> <prompt|cron|interval|once|script> <value>`
- `/usage [run_id]`
- `/reasoning [low|medium|high|xhigh]`
- `/skills`
- `/useskill <name>`
- `/unuseskill <name>`
- `/ctxskills`
- `/agents on|off`
- `/loop <5-field-cron> <prompt>`
- `/loops`
- `/unloop <id>`
- `/whoami`
- `/help`

Plain text in a bound topic behaves like `/run <text>`.

## Agent Messages

During a run, Codex may request same-topic Telegram updates by running:

```sh
servitor-send "I found the failing test and am checking the root cause."
```

Codex may also request that the host attach a file from the workspace:

```sh
servitor-send-file hello.txt "Generated hello.txt"
```

Codex may request host-validated interactive actions:

```sh
servitor-action ask --ref deploy --text "Approve deployment?" --option approve "Approve" --option reject "Reject"
servitor-action react --target source --emoji "👀"
servitor-action schedule create --kind interval --spec 1h --prompt "Check status"
servitor-action message-context --target ctx_abc123 --text "Please review this result"
```

The container only appends JSON lines to the run artifact directory. The trusted host process validates requests, enforces limits, rejects unsafe file paths or unsupported actions, writes audit JSONL files, and executes accepted text/file/action requests in the bound Telegram topic.

## Security Notes

Containers run as a non-root user and do not include sudo. Servitor does not mount the Docker socket, host home, `.env`, `.ssh`, `~/.codex/auth.json`, or arbitrary host paths. The container receives only placeholder credentials and sends OpenAI/Codex API traffic through the host credential proxy.
