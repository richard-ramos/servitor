# Architecture

Servitor is a single Go process with four loops:

- Telegram long polling receives updates and stores messages.
- The credential proxy forwards OpenAI/Codex API traffic and injects host-only ChatGPT OAuth or API-key credentials. In ChatGPT mode, contexts use a generated Codex custom provider named `servitor` so core Responses traffic goes through the proxy.
- The queue loop runs one Docker container per queued prompt.
- The scheduler loop turns cron, interval, and one-shot schedules into queue items.

Durable state is under `DATA_DIR`:

- `servitor.db` stores contexts, per-context reasoning effort, bindings, messages, attachments, queue items, runs, and schedules.
- `contexts/<id>/workspace` is the only durable project workspace visible to Codex.
- `contexts/<id>/.codex` stores per-context Codex config/session data.
- `contexts/<id>/runs/<run_id>` stores prompt/output/log artifacts.

Containers cannot directly call Servitor or Telegram. They produce run artifacts, a final message, and optional JSONL host-action requests that the trusted host validates and audits. Containers also do not receive or mount the host Codex auth cache; they use placeholder credentials against the host proxy.
