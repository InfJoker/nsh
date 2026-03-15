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
- `anthropic` — requires `ANTHROPIC_API_KEY` env var
- `copilot` — GitHub Copilot OAuth (not yet implemented)
- `mock` — for development/testing
