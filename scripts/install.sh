#!/usr/bin/env bash
set -euo pipefail

go build -o servitor .

mkdir -p "$HOME/.config/systemd/user"
cat > "$HOME/.config/systemd/user/servitor.service" <<EOF
[Unit]
Description=Servitor Codex Telegram control plane
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$(pwd)
ExecStart=$(pwd)/servitor
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF

sudo loginctl enable-linger "$USER"
systemctl --user daemon-reload
systemctl --user enable --now servitor.service
