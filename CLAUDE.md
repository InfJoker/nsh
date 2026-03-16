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
- `~/.nsh/config.toml` — created on first run with empty provider (triggers setup)
- `~/.nsh/data/` — history.json, projects.json, auth.json, learned_rules.toml

## Supported Providers

| Provider | Model selection | Runtime |
|---|---|---|
| `anthropic` | Fixed (claude-sonnet) | Anthropic API (requires `ANTHROPIC_API_KEY`) |
| `ollama` | Only shows locally-installed tool-capable models | Ollama daemon (must be running) |
| `llama.cpp` | llmfit recommends for hardware → download → serve | llmfit + llama-server |
| `copilot` | Fixed (not yet implemented) | GitHub Copilot OAuth |
| `mlx` | llmfit recommends for Apple Silicon | mlx_lm.server (requires `pip3 install mlx-lm`) |
| `mock` | Fixed | Built-in (for development/testing) |

### Ollama Provider
- **Simple**: only lists models already pulled locally. No recommendations, no downloads.
- Users manage ollama models themselves (`ollama pull <model>`).
- `!provider` builtin triggers interactive setup that shows tool-capable local models.

### llama.cpp Provider
- **Smart local option**: llmfit handles hardware detection, model recommendation, downloading with optimal quantization, and serving.
- Server lifecycle: `llmfit run <model> --server --port <port>` starts/stops with nsh.
- Auto port: `FindFreePort()` binds `:0` for OS-assigned ephemeral port.
- No name mapping: llmfit download name = serve name = API model name.
- `internal/llm/llmfit_local.go` — runtime management (install, download, serve, port, wait).

### llmfit Integration (llama.cpp only)
- Use `llmfit list --json` to query the model database, filter by `capabilities: ["tool_use"]` + `gguf_sources` (non-empty) + RAM fit.
- Use `llmfit recommend --json` for hardware detection (`system.total_ram_gb`, `system.gpu_vram_gb`, `system.unified_memory`).
- `llmfit` output format: `{"models": [...], "system": {...}}` for `recommend`; top-level array for `list`.

## Development Guidelines
- **No hardcoded model catalogs.** Model availability changes frequently. Always query llmfit or ollama APIs at runtime.
- **Interactive I/O in `internal/llm/`**: The `llm` package contains `RunOllamaSetup()` and `RunLlamaCppSetup()` which do interactive stdin/stdout. When called from the TUI, they must run via `tea.ExecProcess` (subprocess) to avoid conflicting with bubbletea's terminal control.
