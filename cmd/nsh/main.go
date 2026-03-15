package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/anthropics/nsh/internal/agent"
	"github.com/anthropics/nsh/internal/auth"
	"github.com/anthropics/nsh/internal/config"
	"github.com/anthropics/nsh/internal/executor"
	"github.com/anthropics/nsh/internal/llm"
	"github.com/anthropics/nsh/internal/msgs"
	"github.com/anthropics/nsh/internal/shell"
	"github.com/anthropics/nsh/internal/tui"
)

func main() {
	// Check for --exec mode (non-interactive, no TUI)
	if len(os.Args) >= 3 && os.Args[1] == "--exec" {
		query := strings.Join(os.Args[2:], " ")
		runExec(query)
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

	// 7. Create LLM client
	client, err := llm.NewProvider(cfg.Provider, cfg.Model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating LLM client: %v\n", err)
		fmt.Fprintln(os.Stderr, "Starting with mock LLM (set ANTHROPIC_API_KEY for real responses)")
		client = llm.NewMockClient()
	}

	// 8. Create TUI model and program
	model := tui.NewApp(cfg, env, client, history, projects)
	p := tea.NewProgram(model)

	// Inject program reference for agent streaming via program.Send
	model.SetProgram(p)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// 9. Cleanup: save state
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

	client, err := llm.NewProvider(cfg.Provider, cfg.Model)
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

	if hasAnthropicKey {
		fmt.Println("  Detected ANTHROPIC_API_KEY in environment.")
	}
	if hasCopilotToken {
		fmt.Println("  Detected existing GitHub Copilot token.")
	}

	fmt.Println()
	fmt.Println("  Choose an LLM provider:")
	fmt.Println()
	fmt.Println("  [1] anthropic  - Anthropic API (requires ANTHROPIC_API_KEY)")
	fmt.Println("  [2] copilot    - GitHub Copilot (requires Copilot subscription)")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// Default suggestion
	defaultChoice := "1"
	if hasCopilotToken && !hasAnthropicKey {
		defaultChoice = "2"
	}

	fmt.Printf("  Enter choice [%s]: ", defaultChoice)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		input = defaultChoice
	}

	var provider, model string
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

	default:
		fmt.Fprintf(os.Stderr, "  Unknown choice: %q\n", input)
		os.Exit(1)
	}

	cfg.Provider = provider
	cfg.Model = model

	// Persist to config file
	if err := cfg.SaveProvider(provider, model); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: couldn't save provider to config: %v\n", err)
	} else {
		fmt.Printf("\n  Saved provider=%q to ~/.nsh/config.toml\n", provider)
	}

	fmt.Println()
}
