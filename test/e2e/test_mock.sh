#!/bin/bash
# E2E test for mock provider (validates harness works)
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=== Mock E2E Test ==="

"$SCRIPT_DIR/nsh-test.sh" start
"$SCRIPT_DIR/nsh-test.sh" wait "❯" 10
"$SCRIPT_DIR/nsh-test.sh" type "hello"
"$SCRIPT_DIR/nsh-test.sh" key Enter
"$SCRIPT_DIR/nsh-test.sh" wait "help you" 10
"$SCRIPT_DIR/nsh-test.sh" screenshot mock.png
"$SCRIPT_DIR/nsh-test.sh" stop

echo "=== Mock E2E Test PASSED ==="
