#!/bin/bash
set -e
set +H 2>/dev/null || true  # Disable history expansion so ! passes through

CONTAINER="nsh-e2e"
IMAGE="nsh-e2e"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# State file for remembering start flags
STATE_FILE="/tmp/nsh-e2e-state"

cmd_start() {
    local provider="mock"
    local model="claude-sonnet-4-6"
    local base_url=""

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --provider) provider="$2"; shift 2 ;;
            --model) model="$2"; shift 2 ;;
            --base-url) base_url="$2"; shift 2 ;;
            *) echo "Unknown flag: $1"; exit 1 ;;
        esac
    done

    # Save state for reset
    echo "provider=$provider" > "$STATE_FILE"
    echo "model=$model" >> "$STATE_FILE"
    echo "base_url=$base_url" >> "$STATE_FILE"

    echo "Building image..."
    docker build -t "$IMAGE" -f "$SCRIPT_DIR/Dockerfile" "$PROJECT_DIR"

    echo "Starting container (provider=$provider)..."
    local docker_args=(
        run -d
        --name "$CONTAINER"
        -e "NSH_PROVIDER=$provider"
        -e "NSH_MODEL=$model"
    )

    if [[ "$provider" == "anthropic" ]]; then
        if [[ -z "$ANTHROPIC_API_KEY" ]]; then
            echo "Error: ANTHROPIC_API_KEY not set" >&2
            exit 1
        fi
        docker_args+=(-e "ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY")
    fi

    if [[ "$provider" == "ollama" ]]; then
        # Default to host's Ollama instance
        local ollama_url="${OLLAMA_BASE_URL:-http://host.docker.internal:11434/v1}"
        base_url="${base_url:-$ollama_url}"
        docker_args+=(-e "NSH_BASE_URL=$base_url")
        # On Linux, add host.docker.internal mapping
        if [[ "$(uname)" == "Linux" ]]; then
            docker_args+=(--add-host=host.docker.internal:host-gateway)
        fi
    fi

    if [[ -n "$base_url" ]]; then
        docker_args+=(-e "NSH_BASE_URL=$base_url")
    fi

    docker_args+=("$IMAGE")
    docker "${docker_args[@]}"

    # Wait for tmux to be ready
    local attempts=0
    while ! docker exec "$CONTAINER" tmux has-session -t main 2>/dev/null; do
        sleep 0.5
        attempts=$((attempts + 1))
        if [[ $attempts -ge 20 ]]; then
            echo "Error: tmux session did not start" >&2
            exit 1
        fi
    done
    echo "Container ready."
}

cmd_stop() {
    docker rm -f "$CONTAINER" 2>/dev/null || true
    echo "Container stopped."
}

cmd_type() {
    local text="$1"
    if [[ -z "$text" ]]; then
        echo "Usage: nsh-test.sh type \"text\"" >&2
        exit 1
    fi
    # Unescape \! that zsh history expansion adds, then use paste-buffer
    # to avoid tmux send-keys escaping special chars
    text="${text//\\!/!}"
    docker exec "$CONTAINER" bash -c \
        "printf '%s' \"\$1\" | tmux load-buffer - && tmux paste-buffer -t main" \
        -- "$text"
}

cmd_key() {
    local key="$1"
    if [[ -z "$key" ]]; then
        echo "Usage: nsh-test.sh key Enter|Escape|Tab|Up|Down|C-c|C-d" >&2
        exit 1
    fi
    docker exec "$CONTAINER" tmux send-keys -t main "$key"
}

cmd_screen() {
    docker exec "$CONTAINER" tmux capture-pane -t main -p
}

cmd_wait() {
    local pattern="$1"
    local timeout="${2:-10}"

    if [[ -z "$pattern" ]]; then
        echo "Usage: nsh-test.sh wait \"pattern\" [timeout_seconds]" >&2
        exit 1
    fi

    local deadline=$((SECONDS + timeout))
    while [[ $SECONDS -lt $deadline ]]; do
        if docker exec "$CONTAINER" tmux capture-pane -t main -p | grep -q "$pattern"; then
            return 0
        fi
        sleep 0.5
    done

    echo "Timeout waiting for pattern: $pattern" >&2
    echo "Current screen:" >&2
    docker exec "$CONTAINER" tmux capture-pane -t main -p >&2
    return 1
}

cmd_assert() {
    local pattern="$1"
    if [[ -z "$pattern" ]]; then
        echo "Usage: nsh-test.sh assert \"pattern\"" >&2
        exit 1
    fi

    local content
    content=$(docker exec "$CONTAINER" tmux capture-pane -t main -p)
    if echo "$content" | grep -q "$pattern"; then
        echo "PASS: found '$pattern'"
    else
        echo "FAIL: pattern '$pattern' not found" >&2
        echo "Screen content:" >&2
        echo "$content" >&2
        exit 1
    fi
}

cmd_screenshot() {
    local out="${1:-screenshot.png}"
    docker exec "$CONTAINER" bash -c \
        'tmux capture-pane -t main -pe | termshot --raw-read - -f /tmp/screen.png -- true'
    docker cp "$CONTAINER:/tmp/screen.png" "$out"
    echo "Screenshot saved to $out"
}

cmd_reset() {
    local args=()
    if [[ -f "$STATE_FILE" ]]; then
        source "$STATE_FILE"
        args+=(--provider "$provider" --model "$model")
        if [[ -n "${base_url:-}" ]]; then
            args+=(--base-url "$base_url")
        fi
    fi
    cmd_stop
    cmd_start "${args[@]}"
}

# Dispatch
case "${1:-}" in
    start)      shift; cmd_start "$@" ;;
    stop)       cmd_stop ;;
    type)       shift; cmd_type "$@" ;;
    key)        shift; cmd_key "$@" ;;
    screen)     cmd_screen ;;
    wait)       shift; cmd_wait "$@" ;;
    assert)     shift; cmd_assert "$@" ;;
    screenshot) shift; cmd_screenshot "$@" ;;
    reset)      cmd_reset ;;
    *)
        echo "Usage: nsh-test.sh <command> [args]"
        echo ""
        echo "Commands:"
        echo "  start [--provider mock|anthropic|ollama] [--model ...] [--base-url ...]  Start test container"
        echo "  stop                                              Stop test container"
        echo "  type \"text\"                                       Type text into nsh"
        echo "  key Enter|Escape|Tab|Up|Down|C-c|C-d             Send key to nsh"
        echo "  screen                                            Capture screen text"
        echo "  wait \"pattern\" [timeout]                          Wait for pattern (default 10s)"
        echo "  assert \"pattern\"                                  Assert pattern on screen"
        echo "  screenshot [filename]                             Save PNG screenshot"
        echo "  reset                                             Restart with same flags"
        exit 1
        ;;
esac
