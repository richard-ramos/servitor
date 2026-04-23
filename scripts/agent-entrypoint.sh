#!/usr/bin/env bash
set -euo pipefail

ARTIFACT_DIR="${SERVITOR_ARTIFACT_DIR:-/run-artifacts}"
PROMPT_FILE="$ARTIFACT_DIR/prompt.txt"
RESPONSE_FILE="$ARTIFACT_DIR/response.jsonl"
LAST_MESSAGE_FILE="$ARTIFACT_DIR/last_message.txt"
STATUS_FILE="$ARTIFACT_DIR/status.json"
SESSION_FILE="$ARTIFACT_DIR/session_id.txt"

started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '{"state":"running","started_at":"%s"}\n' "$started_at" > "$STATUS_FILE"

if [[ ! -f "$PROMPT_FILE" ]]; then
  printf '{"state":"error","exit_code":2,"started_at":"%s","finished_at":"%s","error":"missing prompt.txt"}\n' "$started_at" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$STATUS_FILE"
  exit 2
fi

session="${SERVITOR_CODEX_SESSION_ID:-}"

set +e
if [[ -n "$session" ]]; then
  codex exec resume --json --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check --output-last-message "$LAST_MESSAGE_FILE" "$session" - < "$PROMPT_FILE" > "$RESPONSE_FILE"
else
  codex exec --json --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check --output-last-message "$LAST_MESSAGE_FILE" - < "$PROMPT_FILE" > "$RESPONSE_FILE"
fi
exit_code="$?"
set -e

python3 - "$RESPONSE_FILE" "$SESSION_FILE" <<'PY' || true
import json, sys
resp, out = sys.argv[1], sys.argv[2]
session = ""
try:
    for line in open(resp, "r", encoding="utf-8", errors="replace"):
        try:
            obj = json.loads(line)
        except Exception:
            continue
        for key in ("session_id", "sessionId", "conversation_id", "conversationId"):
            if isinstance(obj.get(key), str) and obj[key]:
                session = obj[key]
        nested = obj.get("session")
        if isinstance(nested, dict) and isinstance(nested.get("id"), str):
            session = nested["id"]
except FileNotFoundError:
    pass
if session:
    open(out, "w", encoding="utf-8").write(session + "\n")
PY

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
if [[ "$exit_code" -eq 0 ]]; then
  printf '{"state":"done","exit_code":0,"started_at":"%s","finished_at":"%s"}\n' "$started_at" "$finished_at" > "$STATUS_FILE"
else
  printf '{"state":"error","exit_code":%d,"started_at":"%s","finished_at":"%s"}\n' "$exit_code" "$started_at" "$finished_at" > "$STATUS_FILE"
fi
exit "$exit_code"
