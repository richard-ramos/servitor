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
- Cron schedules through `/loop`, `/loops`, and `/unloop`.
- Controlled agent-to-Telegram updates through a host-validated `servitor-send` helper.

Deferred: private repo credentials, host directory contexts, remote workers, swarms, MCP, skills registry, multichannel support, and natural-language scheduling.

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

If `OPENAI_PROXY_HOST=127.0.0.1`, Servitor listens on `0.0.0.0` internally so Docker bridge containers can reach `host.docker.internal`. The proxy requires a per-process placeholder bearer token before injecting real host credentials.

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
- `/run <prompt>`
- `/resume <prompt>`
- `/archive`
- `/tail [n]`
- `/artifacts`
- `/sendfile <workspace-relative-path>`
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

The container only appends a JSON line to the run artifact directory. The trusted host process validates the request, enforces `AGENT_MESSAGE_MAX_PER_RUN` and `AGENT_MESSAGE_MAX_CHARS`, rejects Telegram commands and unsafe file paths, writes `agent_messages_audit.jsonl`, and posts accepted text/files to the current topic.

## Security Notes

Containers run as a non-root user and do not include sudo. Servitor does not mount the Docker socket, host home, `.env`, `.ssh`, `~/.codex/auth.json`, or arbitrary host paths. The container receives only placeholder credentials and sends OpenAI/Codex API traffic through the host credential proxy.
