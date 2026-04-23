#!/usr/bin/env bash
set -euo pipefail

echo "Go: $(go version)"
echo "Docker: $(docker --version)"
docker info >/dev/null

if [[ -f .env ]]; then
  echo ".env: present"
else
  echo ".env: missing"
fi

systemctl --user is-enabled servitor.service || true
systemctl --user is-active servitor.service || true
journalctl --user -u servitor.service -n 50 --no-pager || true
