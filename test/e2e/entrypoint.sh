#!/bin/bash
set -e

mkdir -p ~/.nsh
cat > ~/.nsh/config.toml <<EOF
provider = "${NSH_PROVIDER:-mock}"
model = "${NSH_MODEL:-claude-sonnet-4-20250514}"
EOF

# Start tmux with nsh
tmux new-session -d -s main -x 120 -y 40 /usr/local/bin/nsh

# Keep container alive
sleep infinity
