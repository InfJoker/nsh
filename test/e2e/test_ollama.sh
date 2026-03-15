#!/bin/bash
# E2E test for Ollama provider
# Requires: Ollama running on host with a tool-capable model pulled
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MODEL="${1:-qwen2.5:3b}"

echo "=== Ollama E2E Test (model: $MODEL) ==="

"$SCRIPT_DIR/nsh-test.sh" start --provider ollama --model "$MODEL"
"$SCRIPT_DIR/nsh-test.sh" wait "❯" 10
"$SCRIPT_DIR/nsh-test.sh" type "what files are in the current directory"
"$SCRIPT_DIR/nsh-test.sh" key Enter
"$SCRIPT_DIR/nsh-test.sh" wait "ls" 30
"$SCRIPT_DIR/nsh-test.sh" screenshot ollama.png
"$SCRIPT_DIR/nsh-test.sh" stop

echo "=== Ollama E2E Test PASSED ==="
