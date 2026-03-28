#!/bin/bash
set -e

mkdir -p ~/.nsh

PROVIDER="${NSH_PROVIDER:-mock}"
MODEL="${NSH_MODEL:-mock}"
BASE_URL="${NSH_BASE_URL:-}"

# Build config with presets format
{
    echo 'preset = "default"'
    echo 'theme = "catppuccin"'
    echo 'max_steps = 25'
    echo ''
    echo "[providers.default]"
    echo "type = \"${PROVIDER}\""
    if [[ -n "${BASE_URL}" ]]; then
        echo "base_url = \"${BASE_URL}\""
    fi
    echo ''
    echo "[presets.default]"
    echo 'provider = "default"'
    echo "model = \"${MODEL}\""
} > ~/.nsh/config.toml

# Start tmux with nsh
tmux new-session -d -s main -x 120 -y 40 /usr/local/bin/nsh

# Keep container alive
sleep infinity
