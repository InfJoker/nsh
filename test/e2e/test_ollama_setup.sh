#!/bin/bash
# E2E test: Ollama installation, model pull, detection, and tool-checking inside Docker.
# Tests the actual install.sh flow, ollama serve, model pull, and our Go helpers.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
IMAGE="nsh-ollama-test"
CONTAINER="nsh-ollama-setup-test"

cleanup() {
    docker rm -f "$CONTAINER" 2>/dev/null || true
}
trap cleanup EXIT

echo "=== Ollama Setup E2E Test ==="

# Build image
echo "Building test image..."
docker build -t "$IMAGE" -f "$SCRIPT_DIR/ollama-test.Dockerfile" "$PROJECT_DIR" 2>&1 | tail -3

# Start container in background (keep alive)
docker rm -f "$CONTAINER" 2>/dev/null || true
docker run -d --name "$CONTAINER" "$IMAGE" sleep infinity

echo ""
echo "--- Step 1: Install Ollama via install.sh ---"
docker exec "$CONTAINER" bash -c 'curl -fsSL https://ollama.com/install.sh | sh' 2>&1 | tail -5
echo ""

echo "--- Step 2: Verify ollama binary ---"
docker exec "$CONTAINER" which ollama
docker exec "$CONTAINER" ollama --version
echo ""

echo "--- Step 3: Start ollama serve ---"
docker exec -d "$CONTAINER" bash -c 'ollama serve > /tmp/ollama.log 2>&1'
echo "Waiting for Ollama to be ready..."
attempts=0
while ! docker exec "$CONTAINER" curl -sf http://localhost:11434/api/tags > /dev/null 2>&1; do
    sleep 1
    attempts=$((attempts + 1))
    if [[ $attempts -ge 30 ]]; then
        echo "FAIL: Ollama did not start in 30s" >&2
        docker exec "$CONTAINER" cat /tmp/ollama.log >&2
        exit 1
    fi
done
echo "Ollama ready (${attempts}s)"
echo ""

echo "--- Step 4: Pull a small model (qwen2.5:0.5b) ---"
docker exec "$CONTAINER" ollama pull qwen2.5:0.5b 2>&1
echo ""

echo "--- Step 5: Test Go helpers (DetectOllama, ListModels, ModelSupportsTools) ---"
docker exec "$CONTAINER" bash -c 'cat > /tmp/ollama_test.go << '\''GOEOF'\''
package main

import (
    "fmt"
    "os"

    "github.com/InfJoker/nsh/internal/llm"
)

func main() {
    base := "http://localhost:11434"
    failures := 0

    // Test DetectOllama
    if llm.DetectOllama(base) {
        fmt.Println("PASS: DetectOllama returns true")
    } else {
        fmt.Println("FAIL: DetectOllama returns false")
        failures++
    }

    // Test ListOllamaModels
    models, err := llm.ListOllamaModels(base)
    if err != nil {
        fmt.Printf("FAIL: ListOllamaModels error: %v\n", err)
        failures++
    } else if len(models) == 0 {
        fmt.Println("FAIL: ListOllamaModels returned 0 models")
        failures++
    } else {
        fmt.Printf("PASS: ListOllamaModels found %d model(s):\n", len(models))
        for _, m := range models {
            fmt.Printf("      - %s (%.1f MB)\n", m.Name, float64(m.Size)/(1024*1024))
        }
    }

    // Test ModelSupportsTools
    supports := llm.ModelSupportsTools(base, "qwen2.5:0.5b")
    fmt.Printf("INFO: ModelSupportsTools(qwen2.5:0.5b) = %v\n", supports)
    // qwen2.5 should support tools
    if supports {
        fmt.Println("PASS: qwen2.5:0.5b supports tools")
    } else {
        fmt.Println("WARN: qwen2.5:0.5b does not report tool support (may vary by Ollama version)")
    }

    // Test NewProvider creates successfully
    client, err := llm.NewProvider("ollama", "qwen2.5:0.5b", "http://localhost:11434/v1")
    if err != nil {
        fmt.Printf("FAIL: NewProvider error: %v\n", err)
        failures++
    } else if client == nil {
        fmt.Println("FAIL: NewProvider returned nil client")
        failures++
    } else {
        fmt.Println("PASS: NewProvider(ollama) created successfully")
    }

    if failures > 0 {
        fmt.Printf("\n%d FAILURE(s)\n", failures)
        os.Exit(1)
    }
    fmt.Println("\nAll checks passed!")
}
GOEOF
cd /src && go run /tmp/ollama_test.go'
echo ""

echo "=== Ollama Setup E2E Test COMPLETE ==="
