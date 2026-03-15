package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

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
	client, err := llm.NewProvider(cfg.Provider, cfg.Model, cfg.BaseURL)
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

	// Check Ollama reachability in exec mode
	if cfg.Provider == "ollama" && !llm.DetectOllama("") {
		fmt.Fprintln(os.Stderr, "Ollama not running. Start it with: ollama serve")
		os.Exit(1)
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
	fmt.Println("  [3] ollama     - Ollama (local, private)")
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
		model, baseURL = runOllamaSetup(reader)

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

// runOllamaSetup handles the Ollama-specific setup sub-flow.
// Returns the selected model and base URL.
func runOllamaSetup(reader *bufio.Reader) (model, baseURL string) {
	baseURL = "http://localhost:11434/v1"
	ollamaBase := "http://localhost:11434"

	// Step 1: Ensure Ollama is installed and running
	if !llm.DetectOllama("") {
		if !llm.OllamaInstalled() {
			if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
				fmt.Println()
				fmt.Println("  Ollama is not available on this platform.")
				fmt.Println("  Install manually: https://ollama.com/download")
				os.Exit(1)
			}

			fmt.Println()
			fmt.Println("  Ollama not found.")
			fmt.Println("  Install now? This will run: curl -fsSL https://ollama.com/install.sh | sh")
			fmt.Printf("  [Y/n]: ")
			ans, _ := reader.ReadString('\n')
			ans = strings.TrimSpace(strings.ToLower(ans))
			if ans != "" && ans != "y" && ans != "yes" {
				fmt.Println("  Skipped. Install Ollama manually: https://ollama.com/download")
				os.Exit(0)
			}

			fmt.Println()
			fmt.Println("  Installing Ollama...")
			if err := llm.InstallOllama(); err != nil {
				fmt.Fprintf(os.Stderr, "  Error installing Ollama: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("  ✓ Ollama installed")
		}

		// Ollama installed but not running
		fmt.Println("  Starting Ollama...")
		if err := llm.StartOllama(); err != nil {
			fmt.Fprintf(os.Stderr, "  Error starting Ollama: %v\n", err)
			fmt.Fprintln(os.Stderr, "  Try running: ollama serve")
			os.Exit(1)
		}
		fmt.Println("  ✓ Ollama ready")
	}

	// Step 2: List models and filter by tool-calling capability
	models, err := llm.ListOllamaModels(ollamaBase)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error listing models: %v\n", err)
		os.Exit(1)
	}

	// Filter to tool-capable models
	type modelEntry struct {
		name string
		size int64
	}
	var toolCapable []modelEntry
	for _, m := range models {
		if llm.ModelSupportsTools(ollamaBase, m.Name) {
			toolCapable = append(toolCapable, modelEntry{name: m.Name, size: m.Size})
		}
	}

	// Step 3: If no tool-capable models, try llmfit recommendations or suggest a default
	if len(toolCapable) == 0 {
		fmt.Println()
		fmt.Println("  No tool-calling capable models found.")

		// Try llmfit for recommendations
		suggestedModel := "qwen2.5:7b" // sensible default
		if err := llm.EnsureLlmfit(); err == nil {
			fmt.Println("  Analyzing your hardware...")
			recs, err := llm.RecommendModels(ollamaBase)
			if err == nil && len(recs) > 0 {
				suggestedModel = recs[0].Name
				fmt.Println()
				fmt.Println("  Recommended models (via llmfit, filtered for tool-calling):")
				for i, r := range recs {
					marker := ""
					if i == 0 {
						marker = "  * best fit"
					}
					fmt.Printf("    [%d] %-20s (score: %.0f)%s\n", i+1, r.Name, r.Score, marker)
				}
				fmt.Println()
			}
		}

		fmt.Printf("  Pull and use %s? [Y/n]: ", suggestedModel)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans != "" && ans != "y" && ans != "yes" {
			fmt.Println("  Pull a model manually: ollama pull <model>")
			os.Exit(0)
		}

		fmt.Println()
		fmt.Printf("  Pulling %s...\n", suggestedModel)
		if err := llm.PullModel(suggestedModel); err != nil {
			fmt.Fprintf(os.Stderr, "  Error pulling model: %v\n", err)
			os.Exit(1)
		}
		model = suggestedModel
		return model, baseURL
	}

	// Step 4: Show available tool-capable models, let user pick
	fmt.Println()
	fmt.Println("  Available models (tool-calling capable):")
	for i, m := range toolCapable {
		sizeGB := float64(m.size) / (1024 * 1024 * 1024)
		marker := ""
		if i == 0 {
			marker = "  * recommended"
		}
		fmt.Printf("    [%d] %-20s (%.1f GB)%s\n", i+1, m.name, sizeGB, marker)
	}
	fmt.Println()

	fmt.Printf("  Enter model [1]: ")
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(ans)
	if ans == "" {
		ans = "1"
	}

	idx := 0
	if _, err := fmt.Sscanf(ans, "%d", &idx); err != nil || idx < 1 || idx > len(toolCapable) {
		// Try as model name
		model = ans
	} else {
		model = toolCapable[idx-1].name
	}

	return model, baseURL
}
