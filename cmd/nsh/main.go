package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/InfJoker/nsh/internal/agent"
	"github.com/InfJoker/nsh/internal/auth"
	"github.com/InfJoker/nsh/internal/config"
	"github.com/InfJoker/nsh/internal/executor"
	"github.com/InfJoker/nsh/internal/llm"
	"github.com/InfJoker/nsh/internal/msgs"
	"github.com/InfJoker/nsh/internal/shell"
	"github.com/InfJoker/nsh/internal/tui"
)

func main() {
	// Check for --exec mode (non-interactive, no TUI)
	if len(os.Args) >= 3 && os.Args[1] == "--exec" {
		query := strings.Join(os.Args[2:], " ")
		runExec(query)
		return
	}

	// Hidden flag: --ollama-setup <resultfile> (used by TUI provider switch via tea.ExecProcess)
	if len(os.Args) >= 3 && os.Args[1] == "--ollama-setup" {
		resultFile := os.Args[2]
		result, err := llm.RunOllamaSetup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			os.Exit(1)
		}
		// Write result for the TUI to read back
		os.WriteFile(resultFile, []byte(result.Model+"\n"+result.BaseURL+"\n"), 0600)
		fmt.Println("\n  Press Enter to return to nsh...")
		bufio.NewReader(os.Stdin).ReadByte()
		return
	}

	// Hidden flag: --llamacpp-setup <resultfile> (used by TUI provider switch via tea.ExecProcess)
	if len(os.Args) >= 3 && os.Args[1] == "--llamacpp-setup" {
		resultFile := os.Args[2]
		result, err := llm.RunLlamaCppSetup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			os.Exit(1)
		}
		// Write only the model name — port allocation happens in the parent process
		os.WriteFile(resultFile, []byte(result.Model+"\n"), 0600)
		fmt.Println("\n  Press Enter to return to nsh...")
		bufio.NewReader(os.Stdin).ReadByte()
		return
	}

	// Hidden flag: --mlx-setup <resultfile> (used by TUI provider switch via tea.ExecProcess)
	if len(os.Args) >= 3 && os.Args[1] == "--mlx-setup" {
		resultFile := os.Args[2]
		result, err := llm.RunMLXSetup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			os.Exit(1)
		}
		// Write only the model name (HF repo ID) — port allocation happens in the parent process
		os.WriteFile(resultFile, []byte(result.Model+"\n"), 0600)
		fmt.Println("\n  Press Enter to return to nsh...")
		bufio.NewReader(os.Stdin).ReadByte()
		return
	}

	// Non-TTY guard
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "nsh requires an interactive terminal. Use your regular shell for scripts.")
		os.Exit(1)
	}

	// 1. Load config
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// 2. If no provider configured, run first-time setup
	if cfg.Provider == "" {
		runProviderSetup(cfg)
	}

	// 3. Auth check (if provider requires it)
	if cfg.Provider == "copilot" && !auth.HasValidToken() {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		_, err := auth.RunDeviceFlow(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Authentication failed: %v\n", err)
			os.Exit(1)
		}
	}

	// 4. Initialize environment state
	env := shell.NewEnvState()

	// 5. Load project index
	projects := shell.NewProjectIndex()

	// 6. Load history
	history := agent.NewHistory()
	history.LoadInputHistory()

	// 7. For local providers, reuse a shared server or start a new one.
	// Reference-counted via flock — last nsh to exit kills the server.
	var usingSharedServer bool

	if cfg.Provider == "mlx" || cfg.Provider == "llama.cpp" {
		baseURL, err := llm.AcquireSharedServer(cfg.Provider, cfg.Model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error acquiring server: %v\n", err)
			os.Exit(1)
		}

		if baseURL != "" {
			// Reusing existing server (refcount already incremented)
			cfg.BaseURL = baseURL
			usingSharedServer = true
			fmt.Fprintf(os.Stderr, "Reusing %s server for %s\n", cfg.Provider, cfg.Model)
		} else {
			// Start a new server
			port, err := llm.FindFreePort()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error finding free port: %v\n", err)
				os.Exit(1)
			}
			cfg.BaseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", port)

			fmt.Fprintf(os.Stderr, "Starting %s server for %s on port %d...\n", cfg.Provider, cfg.Model, port)
			var serverCmd *exec.Cmd
			if cfg.Provider == "mlx" {
				serverCmd, err = llm.StartMlxServer(cfg.Model, port)
			} else {
				serverCmd, err = llm.StartLlamaServer(cfg.Model, port)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
				os.Exit(1)
			}

			timeout := 30 * time.Minute
			if cfg.Provider == "mlx" {
				timeout = time.Hour
			}
			if err := llm.WaitForServer(cfg.BaseURL, timeout); err != nil {
				llm.StopServer(serverCmd)
				fmt.Fprintf(os.Stderr, "Server did not become ready: %v\n", err)
				os.Exit(1)
			}

			if cfg.Provider == "llama.cpp" {
				if servedModel, err := llm.QueryServedModel(cfg.BaseURL); err == nil {
					cfg.Model = servedModel
				}
			}

			// Register (or reuse if another process won the race)
			actualURL, regErr := llm.RegisterSharedServer(&llm.ServerInfo{
				Provider: cfg.Provider,
				Model:    cfg.Model,
				PID:      serverCmd.Process.Pid,
				Port:     port,
				BaseURL:  cfg.BaseURL,
			})
			if regErr != nil {
				fmt.Fprintf(os.Stderr, "Error registering server: %v\n", regErr)
				os.Exit(1)
			}
			cfg.BaseURL = actualURL
			usingSharedServer = true
			fmt.Fprintln(os.Stderr, "Server ready")
		}
	}

	// 8. Create LLM client
	client, err := llm.NewProvider(cfg.Provider, cfg.Model, cfg.BaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating LLM client: %v\n", err)
		fmt.Fprintln(os.Stderr, "Starting with mock LLM (set ANTHROPIC_API_KEY for real responses)")
		client = llm.NewMockClient()
	}

	// 9. Create TUI model and program
	model := tui.NewApp(cfg, env, client, history, projects)
	p := tea.NewProgram(model)

	// Inject program reference for agent streaming via program.Send
	model.SetProgram(p)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Decrement refcount — if we're the last user, server is killed atomically.
	if usingSharedServer {
		llm.ReleaseSharedServer()
	}

	// 10. Cleanup: save state
	if err := history.SaveInputHistory(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save history: %v\n", err)
	}
	if err := projects.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save project index: %v\n", err)
	}
}

// runExec runs a single query through the agent loop without the TUI.
func runExec(query string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if cfg.Provider == "" {
		cfg.Provider = "anthropic"
		cfg.Model = "claude-sonnet-4-20250514"
	}

	env := shell.NewEnvState()
	history := agent.NewHistory()

	// Check Ollama reachability in exec mode
	if cfg.Provider == "ollama" && !llm.DetectOllama("") {
		fmt.Fprintln(os.Stderr, "Ollama not running. Start it with: ollama serve")
		os.Exit(1)
	}

	// For local providers in exec mode, reuse shared server or start one
	var execUsingShared bool
	if cfg.Provider == "mlx" || cfg.Provider == "llama.cpp" {
		baseURL, acquireErr := llm.AcquireSharedServer(cfg.Provider, cfg.Model)
		if acquireErr != nil {
			fmt.Fprintf(os.Stderr, "Error acquiring server: %v\n", acquireErr)
			os.Exit(1)
		}
		if baseURL != "" {
			cfg.BaseURL = baseURL
			execUsingShared = true
		} else {
			port, portErr := llm.FindFreePort()
			if portErr != nil {
				fmt.Fprintf(os.Stderr, "Error finding free port: %v\n", portErr)
				os.Exit(1)
			}
			cfg.BaseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", port)
			var serverCmd *exec.Cmd
			if cfg.Provider == "mlx" {
				serverCmd, err = llm.StartMlxServer(cfg.Model, port)
			} else {
				serverCmd, err = llm.StartLlamaServer(cfg.Model, port)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
				os.Exit(1)
			}
			timeout := 30 * time.Minute
			if cfg.Provider == "mlx" {
				timeout = time.Hour
			}
			if err := llm.WaitForServer(cfg.BaseURL, timeout); err != nil {
				llm.StopServer(serverCmd)
				fmt.Fprintf(os.Stderr, "Server did not become ready: %v\n", err)
				os.Exit(1)
			}
			if cfg.Provider == "llama.cpp" {
				if servedModel, err := llm.QueryServedModel(cfg.BaseURL); err == nil {
					cfg.Model = servedModel
				}
			}
			actualURL, regErr := llm.RegisterSharedServer(&llm.ServerInfo{
				Provider: cfg.Provider,
				Model:    cfg.Model,
				PID:      serverCmd.Process.Pid,
				Port:     port,
				BaseURL:  cfg.BaseURL,
			})
			if regErr != nil {
				fmt.Fprintf(os.Stderr, "Error registering server: %v\n", regErr)
				os.Exit(1)
			}
			cfg.BaseURL = actualURL
			execUsingShared = true
		}
	}

	client, err := llm.NewProvider(cfg.Provider, cfg.Model, cfg.BaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating LLM client: %v\n", err)
		os.Exit(1)
	}

	// sendMsg prints agent output to stdout/stderr
	sendMsg := func(msg any) {
		switch m := msg.(type) {
		case msgs.TokenMsg:
			fmt.Print(m.Text)
		case msgs.StreamDoneMsg:
			fmt.Println()
		case msgs.ToolCallStartMsg:
			fmt.Fprintf(os.Stderr, "→ %s: %s\n", m.Name, m.Desc)
		case msgs.ToolCallDoneMsg:
			fmt.Fprintf(os.Stderr, "✓ %s\n", m.Name)
		case msgs.CommandOutputMsg:
			if m.IsStderr {
				fmt.Fprintln(os.Stderr, m.Line)
			} else {
				fmt.Println(m.Line)
			}
		case msgs.AgentErrorMsg:
			fmt.Fprintf(os.Stderr, "Error: %v\n", m.Err)
		case msgs.AgentDoneMsg:
			// done
		}
	}

	// Auto-allow all commands in exec mode
	permFn := func(ctx context.Context, cmd string, isDangerous bool) (msgs.PermissionResponse, error) {
		if isDangerous {
			fmt.Fprintf(os.Stderr, "⚠ Auto-denied dangerous command: %s\n", cmd)
			return msgs.PermissionDeny, nil
		}
		return msgs.PermissionOnce, nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	a := agent.NewAgent(client, history, env, cfg, cfg.Shell, sendMsg, executor.PermissionFunc(permFn))
	projects := shell.NewProjectIndex()
	a.SetProjects(projects)

	a.Run(ctx, query)

	if execUsingShared {
		llm.ReleaseSharedServer()
	}
}

// runProviderSetup handles first-time provider selection.
func runProviderSetup(cfg *config.Config) {
	fmt.Println()
	fmt.Println("  Welcome to nsh!")
	fmt.Println("  ───────────────")
	fmt.Println()

	// Auto-detect available providers
	hasAnthropicKey := os.Getenv("ANTHROPIC_API_KEY") != ""
	hasCopilotToken := auth.HasValidToken()
	hasOllama := llm.DetectOllama("")

	if hasAnthropicKey {
		fmt.Println("  Detected ANTHROPIC_API_KEY in environment.")
	}
	if hasCopilotToken {
		fmt.Println("  Detected existing GitHub Copilot token.")
	}
	if hasOllama {
		fmt.Println("  Detected Ollama at localhost:11434")
	}

	fmt.Println()
	fmt.Println("  Choose an LLM provider:")
	fmt.Println()
	fmt.Println("  [1] anthropic  - Anthropic API (requires ANTHROPIC_API_KEY)")
	fmt.Println("  [2] copilot    - GitHub Copilot (requires Copilot subscription)")
	fmt.Println("  [3] ollama     - Ollama (local, uses already-installed models)")
	fmt.Println("  [4] llama.cpp  - llama.cpp (local, auto-downloads from HuggingFace)")
	fmt.Println("  [5] mlx        - MLX on Apple Silicon (local, auto-downloads best model)")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// Default suggestion
	defaultChoice := "1"
	if hasOllama && !hasAnthropicKey {
		defaultChoice = "3"
	} else if hasCopilotToken && !hasAnthropicKey {
		defaultChoice = "2"
	}

	fmt.Printf("  Enter choice [%s]: ", defaultChoice)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		input = defaultChoice
	}

	var provider, model, baseURL string
	switch input {
	case "1", "anthropic":
		provider = "anthropic"
		model = "claude-sonnet-4-20250514"

		if !hasAnthropicKey {
			fmt.Println()
			fmt.Println("  ANTHROPIC_API_KEY not found in environment.")
			fmt.Println("  Set it before running nsh:")
			fmt.Println("    export ANTHROPIC_API_KEY=sk-ant-...")
			fmt.Println()
		}

	case "2", "copilot":
		provider = "copilot"
		model = "claude-sonnet-4-20250514"

	case "3", "ollama":
		provider = "ollama"
		result, err := llm.RunOllamaSetup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			os.Exit(1)
		}
		model, baseURL = result.Model, result.BaseURL

	case "4", "llama.cpp":
		provider = "llama.cpp"
		result, err := llm.RunLlamaCppSetup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			os.Exit(1)
		}
		model = result.Model
		// baseURL will be set at startup when the server starts

	case "5", "mlx":
		provider = "mlx"
		result, err := llm.RunMLXSetup()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
			os.Exit(1)
		}
		model = result.Model
		// baseURL will be set at startup when the server starts

	default:
		fmt.Fprintf(os.Stderr, "  Unknown choice: %q\n", input)
		os.Exit(1)
	}

	cfg.Provider = provider
	cfg.Model = model
	cfg.BaseURL = baseURL

	// Persist to config file
	if err := cfg.SaveProviderFull(provider, model, baseURL); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: couldn't save provider to config: %v\n", err)
	} else {
		fmt.Printf("\n  Saved provider=%q, model=%q to ~/.nsh/config.toml\n", provider, model)
	}

	fmt.Println()
}

