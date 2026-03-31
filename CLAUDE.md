# nsh - Natural Language Shell

## Project Overview
A natural language shell that replaces bash/zsh. All input goes to LLM by default; `!` prefix runs raw commands directly via `tea.ExecProcess` (full terminal passthrough).

## Tech Stack
- Go 1.25+ with bubbletea v2 (`charm.land/bubbletea/v2`) for TUI
- `charm.land/lipgloss/v2` for styling
- Anthropic SDK (`github.com/anthropics/anthropic-sdk-go`) for LLM streaming
- `mvdan.cc/sh/v3` for shell AST parsing (permission chain splitting)
- `github.com/BurntSushi/toml` for config

## Architecture
- `cmd/nsh/main.go` - Entry point, first-run setup, `--exec` mode
- `internal/msgs/` - Shared message types (agent <-> TUI contract)
- `internal/tui/` - Bubbletea UI (Elm architecture)
- `internal/agent/` - Agent loop, tools, prompt, history
- `internal/llm/` - LLMClient interface, Anthropic provider, mock
- `internal/shell/` - Dispatch, EnvState, builtins, frecency projects
- `internal/executor/` - Captured execution, interactive passthrough, permissions
- `internal/auth/` - GitHub Copilot OAuth device flow
- `internal/config/` - TOML config loading (~/.nsh/config.toml)

## Key Patterns
- **ProgramRef**: Shared `atomic.Pointer[tea.Program]` so both main.go's Model copy and bubbletea's internal copy reference the same `*tea.Program`. Solves Go value-semantics issue with `tea.NewProgram`.
- **Agent streaming via program.Send**: Agent goroutine sends `msgs.*` to TUI via `program.Send()`. `AgentDoneMsg` delivered once via deferred `sendMsg` in `Agent.Run()` — the `tea.Cmd` returns nil.
- **EnvState has sync.RWMutex**: All fields private, accessed via `GetCwd`/`SetCwd`/`ChangeCwd`/`Get`/`Set` etc. `ChangeCwd` atomically updates cwd+PWD+OLDPWD under one lock.
- **Permission checks use shell AST**: `mvdan.cc/sh/v3/syntax` parses compound commands, evaluates each segment. Strictest wins (deny > ask > allow).
- **`!` commands always use tea.ExecProcess**: No interactive detection list — full terminal passthrough for all direct commands. The LLM chooses `launch_interactive` vs `run_command` itself.
- **Dispatch is classification-only**: `shell.Dispatch()` calls `IsBuiltin()` (pure check), never executes.

## Build & Run
```bash
go build -o nsh ./cmd/nsh && ./nsh
```

## Non-interactive mode
```bash
./nsh --exec "show me disk usage"
```

## Test
```bash
go test ./...
```

## E2E Testing (Docker + tmux)
Interactive TUI testing using `test/e2e/nsh-test.sh` — a Playwright-like CLI that runs nsh inside Docker with tmux for programmatic terminal control and termshot for PNG screenshots.

**Requirements**: Docker

**Commands**:
| Command | What it does |
|---|---|
| `start [--provider mock\|anthropic]` | Build image + run container (default: mock) |
| `stop` | Stop + remove container |
| `type "text"` | Type text into nsh |
| `key Enter\|Escape\|Tab\|Up\|Down\|C-c` | Send special key |
| `screen` | Capture screen as plain text |
| `wait "pattern" [timeout]` | Poll screen until pattern matches (default 10s) |
| `assert "pattern"` | Grep screen, exit 1 if not found |
| `screenshot [filename]` | Save PNG screenshot (default: screenshot.png) |
| `reset` | Restart container with same flags |

**Example — mock provider** (fast, no API key):
```bash
./test/e2e/nsh-test.sh start
./test/e2e/nsh-test.sh wait "nsh" 10
./test/e2e/nsh-test.sh type '!echo hello'
./test/e2e/nsh-test.sh key Enter
./test/e2e/nsh-test.sh wait "hello" 5
./test/e2e/nsh-test.sh screenshot boot.png
./test/e2e/nsh-test.sh stop
```

**Example — anthropic provider** (real LLM, requires `ANTHROPIC_API_KEY`):
```bash
./test/e2e/nsh-test.sh start --provider anthropic
./test/e2e/nsh-test.sh type 'what is 2+2'
./test/e2e/nsh-test.sh key Enter
./test/e2e/nsh-test.sh wait "4" 30
./test/e2e/nsh-test.sh screenshot result.png
./test/e2e/nsh-test.sh stop
```

**Viewing screenshots**: Use the Read tool on the PNG file path.

## Config
- `~/.nsh/config.toml` — providers + presets format (see below)
- `~/.nsh/data/` — history.json, projects.json, auth.json, learned_rules.toml, server.json, server.lock

### Config Format (providers + presets)
```toml
preset = "light"                    # active preset

[providers.anthropic]
type = "anthropic"
api_key = "sk-ant-..."             # or use env: ANTHROPIC_API_KEY

[providers.local-mlx]
type = "mlx"                       # native (shared server, ephemeral port)

[providers.local-llama]
type = "llama.cpp"                 # native (shared server, ephemeral port)

[presets.light]
provider = "local-mlx"
model = "mlx-community/Qwen3.5-1B-MLX-4bit"

[presets.cloud]
provider = "anthropic"
model = "claude-sonnet-4-6"
```

- **Providers** = infrastructure config (type, base_url, api_key)
- **Presets** = model selection (references a provider by name)
- `nsh --preset light` to select at launch, `nsh --preset` for interactive picker
- `!presets` builtin to switch in TUI

## Supported Providers

| Provider | Model selection | Runtime |
|---|---|---|
| `anthropic` | Fixed (claude-sonnet-4-6) | Anthropic API (requires `ANTHROPIC_API_KEY`) |
| `ollama` | Only shows locally-installed tool-capable models | Ollama daemon (must be running) |
| `llama.cpp` | User provides HF GGUF repo | llama-server (auto-downloads via `--hf-repo`) |
| `copilot` | Fixed (not yet implemented) | GitHub Copilot OAuth |
| `mlx` | User provides HF MLX model | mlx_lm.server (requires `pip3 install mlx-lm`) |
| `hypura` | User provides local GGUF path | Hypura server (Apple Silicon, models larger than RAM) |
| `mock` | Fixed | Built-in (for development/testing) |

### Ollama Provider
- **Simple**: only lists models already pulled locally. No recommendations, no downloads.
- Users manage ollama models themselves (`ollama pull <model>`).
- `!provider` builtin triggers interactive setup that shows tool-capable local models.

### llama.cpp Provider
- Uses `llama-server --hf-repo` for auto-download from HuggingFace.
- Server lifecycle managed by shared server system (`internal/llm/shared_server.go`).
- Auto port: `FindFreePort()` binds `:0` for OS-assigned ephemeral port.
- `internal/llm/server.go` — server startup, process group kill, wait, query.

### Shared Server System
- Servers (llama.cpp, MLX, Hypura) are shared across nsh instances via flock-based reference counting.
- `~/.nsh/data/server.json` stores PID, port, client PIDs.
- `AcquireSharedServer` / `RegisterSharedServer` / `ReleaseSharedServer` in `shared_server.go`.
- Dead client PIDs pruned on acquire (self-heals after crashes).
- Process group kill via `Setpgid` + `Getpgid` ensures children are cleaned up.

### Hypura Provider
- **Apple Silicon only**: Distributes model tensors across GPU/RAM/NVMe for models that exceed physical memory.
- Uses Ollama-native API (`/api/chat`, `/api/tags`) — NOT OpenAI-compatible `/v1/`.
- `OllamaAPIProvider` in `internal/llm/ollama_api.go` handles NDJSON streaming.
- Server lifecycle: `hypura serve <model.gguf> --host 127.0.0.1 --port <port>`.
- Model specified as local GGUF file path (no HuggingFace auto-download).
- Shared server refcounting same as llama.cpp/MLX.
- Tool calling: Hypura does not parse tool calls from model text output into structured `tool_calls` — needs the text tool parser (see TODO below).

## Development Guidelines
- **No hardcoded model catalogs.** Model availability changes frequently. Always query llmfit or ollama APIs at runtime.
- **Interactive I/O in `internal/llm/`**: The `llm` package contains `RunOllamaSetup()` and `RunLlamaCppSetup()` which do interactive stdin/stdout. When called from the TUI, they must run via `tea.ExecProcess` (subprocess) to avoid conflicting with bubbletea's terminal control.

## Known Issues / TODOs

### MLX tool calling text parser (TODO)
mlx_lm.server has native tool call parsing (via `tool_parsers/`), but many models don't generate the expected `<tool_call>` token — they output tool calls as text (JSON, XML, or custom formats) instead. This affects all Qwen2.5-Coder models (7B and 14B tested). Need a text tool parser in `internal/llm/` that:
- Detects tool call JSON/XML in streamed text content (when `tool_calls` array is empty)
- Supported formats: `<tool_call>{...}</tool_call>`, `` ```json\n{...}\n``` ``, `<function name="..." arguments='...'/>`, bare `{"name":"...","arguments":{...}}`
- Strips leaked stop tokens (`<|im_end|>`, `<|im_start|>`, `</s>`, `<|eot_id|>`)
- Silence mlx_lm server stderr logs after startup (noisy per-request KV cache / HTTP logs pollute TUI)
- Check mlx-lm version ≥0.30.1 during setup (tool parsers improved significantly)
- Add verified model list and warn users that models <14B often fail tool calling
