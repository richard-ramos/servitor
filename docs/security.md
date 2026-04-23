# Security

Servitor follows the following boundaries:

- The host process is trusted.
- Telegram content, filenames, repo URLs, and Codex output are untrusted.
- Containers are sandboxed and receive no real ChatGPT OAuth token or OpenAI API key.

The credential proxy strips client auth headers and injects host-only credentials for allowed OpenAI/Codex requests. The default mode reads the host Codex `auth.json` access token after `codex login --device-auth`; API-key mode is an explicit fallback. In ChatGPT mode, Servitor writes a container-side custom Codex provider that points at the proxy with `requires_openai_auth = true` and a placeholder JWT-shaped token. Proxy logs include method, path, status, and duration, but not request or response bodies.

`~/.codex/auth.json` is never mounted into containers. Treat it like a password because it contains refreshable login credentials.

All Docker mounts are derived from `DATA_DIR` and validated before container start. Host home directories, `.env`, SSH keys, cloud config, and the Docker socket are not mounted.

Codex is launched with its own command sandbox disabled because Docker is the sandbox boundary. This avoids nested bubblewrap namespace failures inside the container while still keeping Codex confined to the non-root container, fixed mounts, and Docker resource limits.

Containers have one narrow host-action channel: they may request same-topic Telegram text updates through `servitor-send` and same-topic workspace file uploads through `servitor-send-file`. The host validates and audits those requests before posting. File uploads must resolve to regular files inside the bound context workspace and obey `MAX_ATTACHMENT_BYTES`. Containers still cannot create schedules, bind topics, archive contexts, send Telegram commands, choose another chat/topic, or spawn sibling Servitor jobs.

`servitor-send` and `servitor-send-file` are intentionally limited to the current run's topic. They are not a general MCP server and do not expose arbitrary Servitor tools.
