#!/bin/bash
set -e

mkdir -p ~/.nsh

# Build config, only include base_url if set
{
    echo "provider = \"${NSH_PROVIDER:-mock}\""
    echo "model = \"${NSH_MODEL:-claude-sonnet-4-20250514}\""
    if [[ -n "${NSH_BASE_URL:-}" ]]; then
        echo "base_url = \"${NSH_BASE_URL}\""
    fi
} > ~/.nsh/config.toml

# Start tmux with nsh
tmux new-session -d -s main -x 120 -y 40 /usr/local/bin/nsh

# Keep container alive
sleep infinity
