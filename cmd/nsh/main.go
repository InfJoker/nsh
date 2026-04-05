package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
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
	// Supports: nsh --exec "query" [--preset name]
	if len(os.Args) >= 3 && os.Args[1] == "--exec" {
		var query, execPreset string
		args := os.Args[2:]
		var queryParts []string
		for i := 0; i < len(args); i++ {
			if args[i] == "--preset" && i+1 < len(args) {
				execPreset = args[i+1]
				i++
			} else {
				queryParts = append(queryParts, args[i])
			}
		}
		query = strings.Join(queryParts, " ")
		runExec(query, execPreset)
		return
	}

	// Hidden setup flags (used by TUI provider switch via tea.ExecProcess)
	setupHandlers := map[string]func() (string, error){
		"--ollama-setup": func() (string, error) {
			r, err := llm.RunOllamaSetup()
			if err != nil {
				return "", err
			}
			return r.Model + "\n" + r.BaseURL + "\n", nil
		},
		"--llamacpp-setup": func() (string, error) {
			r, err := llm.RunLlamaCppSetup()
			if err != nil {
				return "", err
			}
			return r.Model + "\n", nil
		},
		"--mlx-setup": func() (string, error) {
			r, err := llm.RunMLXSetup()
			if err != nil {
				return "", err
			}
			return r.Model + "\n", nil
		},
		"--hypura-setup": func() (string, error) {
			r, err := llm.RunHypuraSetup()
			if err != nil {
				return "", err
			}
			return r.Model + "\n", nil
		},
	}
	if len(os.Args) >= 3 {
		if handler, ok := setupHandlers[os.Args[1]]; ok {
			resultFile := os.Args[2]
			result, err := handler()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %v\n", err)
				os.Exit(1)
			}
			if err := os.WriteFile(resultFile, []byte(result), 0600); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing setup result: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("\n  Press Enter to return to nsh...")
			bufio.NewReader(os.Stdin).ReadByte()
			return
		}
	}

	// Non-TTY guard
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr, "nsh requires an interactive terminal. Use your regular shell for scripts.")
		os.Exit(1)
	}

	// 1. Parse --preset flag
	// --preset <name> → use that preset
	// --preset (bare, no name) → show interactive picker
	var presetFlag string
	presetPicker := false
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--preset" {
			if i+1 < len(os.Args) && !strings.HasPrefix(os.Args[i+1], "--") {
				presetFlag = os.Args[i+1]
				i++
			} else {
				presetPicker = true
			}
		}
	}

	// 2. Load config
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// 3. --preset with name → override active preset
	if presetFlag != "" {
		cfg.ActivePreset = presetFlag
	}

	// 4. --preset without name → interactive picker before TUI
	if presetPicker && cfg.HasPresets() {
		chosen := runPresetPicker(cfg)
		if chosen != "" {
			cfg.ActivePreset = chosen
		}
	}

	// 5. Resolve preset → provider/model, or run first-time setup
	if cfg.HasPresets() {
		if err := cfg.ResolveActivePreset(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Fprintln(os.Stderr, "Available presets:")
			for name, p := range cfg.ListPresets() {
				fmt.Fprintf(os.Stderr, "  %s → %s/%s\n", name, p.Provider, p.Model)
			}
			os.Exit(1)
		}
	} else {
		// No presets configured — run first-time setup
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
	usingSharedServer := ensureLocalServer(cfg, true)

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
func runExec(query string, presetFlag string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if presetFlag != "" {
		cfg.ActivePreset = presetFlag
	}

	if cfg.HasPresets() {
		if err := cfg.ResolveActivePreset(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		cfg.Provider = "anthropic"
		cfg.Model = "claude-sonnet-4-6"
	}

	env := shell.NewEnvState()
	history := agent.NewHistory()

	// Check Ollama reachability in exec mode
	if cfg.Provider == "ollama" && !llm.DetectOllama("") {
		fmt.Fprintln(os.Stderr, "Ollama not running. Start it with: ollama serve")
		os.Exit(1)
	}

	// For local providers in exec mode, reuse shared server or start one
	execUsingShared := ensureLocalServer(cfg, false)

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
	hasHypura := llm.HypuraInstalled()

	if hasAnthropicKey {
		fmt.Println("  Detected ANTHROPIC_API_KEY in environment.")
	}
	if hasCopilotToken {
		fmt.Println("  Detected existing GitHub Copilot token.")
	}
	if hasOllama {
		fmt.Println("  Detected Ollama at localhost:11434")
	}
	hasApfel := llm.ApfelInstalled()
	if hasHypura {
		fmt.Println("  Detected Hypura on PATH")
	}
	if hasApfel {
		fmt.Println("  Detected apfel on PATH")
	}

	fmt.Println()
	fmt.Println("  Choose an LLM provider:")
	fmt.Println()
	fmt.Println("  [1] anthropic  - Anthropic API (requires ANTHROPIC_API_KEY)")
	fmt.Println("  [2] copilot    - GitHub Copilot (requires Copilot subscription)")
	fmt.Println("  [3] ollama     - Ollama (local, uses already-installed models)")
	fmt.Println("  [4] llama.cpp  - llama.cpp (local, auto-downloads from HuggingFace)")
	fmt.Println("  [5] mlx        - MLX on Apple Silicon (local, auto-downloads best model)")
	fmt.Println("  [6] apfel      - Apple on-device model (macOS 26+, Apple Silicon)")
	fmt.Println("  [7] hypura     - Hypura on Apple Silicon (local, runs models larger than RAM)")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)

	// Default suggestion
	defaultChoice := "1"
	if hasApfel && !hasAnthropicKey {
		defaultChoice = "6"
	} else if hasOllama && !hasAnthropicKey {
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
		model = "claude-sonnet-4-6"

		if !hasAnthropicKey {
			fmt.Println()
			fmt.Println("  ANTHROPIC_API_KEY not found in environment.")
			fmt.Println("  Set it before running nsh:")
			fmt.Println("    export ANTHROPIC_API_KEY=sk-ant-...")
			fmt.Println()
		}

	case "2", "copilot":
		provider = "copilot"
		model = "claude-sonnet-4-6"

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

	case "6", "apfel":
		provider = "apfel"
		model = "apple-foundationmodel"

	case "7", "hypura":
		provider = "hypura"
		result, err := llm.RunHypuraSetup()
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

	// Create provider and preset
	provName := provider
	cfg.SaveProvider(provName, config.ProviderConfig{
		Type:    provider,
		BaseURL: baseURL,
	})
	cfg.SavePreset("default", config.Preset{
		Provider: provName,
		Model:    model,
	})
	cfg.SetActivePreset("default")

	// Resolve into runtime fields
	cfg.Provider = provider
	cfg.Model = model
	cfg.BaseURL = baseURL

	fmt.Printf("\n  Saved preset \"default\" → %s/%s to ~/.nsh/config.toml\n", provider, model)

	fmt.Println()
}

// runPresetPicker shows an interactive numbered list of presets before the TUI starts.
// Returns the chosen preset name, or "" to use the default.
func runPresetPicker(cfg *config.Config) string {
	reader := bufio.NewReader(os.Stdin)
	presets := cfg.ListPresets()
	if len(presets) == 0 {
		return ""
	}

	// Build sorted list (active preset first)
	type entry struct {
		name     string
		preset   config.Preset
		isActive bool
	}
	var entries []entry
	for name, p := range presets {
		entries = append(entries, entry{name, p, name == cfg.ActivePreset})
	}
	// Sort: active first, then alphabetical
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isActive != entries[j].isActive {
			return entries[i].isActive
		}
		return entries[i].name < entries[j].name
	})

	fmt.Println()
	fmt.Println("  nsh — select a preset:")
	fmt.Println()
	defaultIdx := 1
	for i, e := range entries {
		marker := ""
		if e.isActive {
			marker = "  ✓ last used"
			defaultIdx = i + 1
		}
		// Resolve provider type for display
		provType := e.preset.Provider
		if cfg.Providers != nil {
			if pc, ok := cfg.Providers[e.preset.Provider]; ok {
				provType = pc.Type
			}
		}
		fmt.Printf("  [%d] %-12s %-10s %s%s\n", i+1, e.name, provType, e.preset.Model, marker)
	}
	fmt.Println()

	fmt.Printf("  Enter preset [%d]: ", defaultIdx)
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return entries[defaultIdx-1].name
	}

	// Try as number
	idx := 0
	if _, err := fmt.Sscanf(ans, "%d", &idx); err == nil && idx >= 1 && idx <= len(entries) {
		return entries[idx-1].name
	}

	// Try as preset name
	for _, e := range entries {
		if e.name == ans {
			return e.name
		}
	}

	fmt.Fprintf(os.Stderr, "  Unknown preset: %q\n", ans)
	os.Exit(1)
	return ""
}

// ensureLocalServer acquires or starts a shared server for local providers (mlx, llama.cpp, apfel, hypura).
// Modifies cfg.BaseURL and cfg.Model in place. Returns true if a shared server is in use.
// When verbose is true, prints status messages to stderr.
func ensureLocalServer(cfg *config.Config, verbose bool) bool {
	if cfg.Provider != "mlx" && cfg.Provider != "llama.cpp" && cfg.Provider != "hypura" && cfg.Provider != "apfel" {
		return false
	}

	baseURL, err := llm.AcquireSharedServer(cfg.Provider, cfg.Model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error acquiring server: %v\n", err)
		os.Exit(1)
	}

	if baseURL != "" {
		cfg.BaseURL = baseURL
		if verbose {
			fmt.Fprintf(os.Stderr, "Reusing %s server for %s\n", cfg.Provider, cfg.Model)
		}
		return true
	}

	port, err := llm.FindFreePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding free port: %v\n", err)
		os.Exit(1)
	}

	if cfg.Provider == "hypura" {
		cfg.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	} else {
		cfg.BaseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", port)
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "Starting %s server for %s on port %d...\n", cfg.Provider, cfg.Model, port)
	}

	var serverCmd *exec.Cmd
	switch cfg.Provider {
	case "mlx":
		serverCmd, err = llm.StartMlxServer(cfg.Model, port)
	case "hypura":
		serverCmd, err = llm.StartHypuraServer(cfg.Model, port)
	case "apfel":
		serverCmd, err = llm.StartApfelServer(port)
	default:
		serverCmd, err = llm.StartLlamaServer(cfg.Model, port)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
		os.Exit(1)
	}

	timeout := 30 * time.Minute
	if cfg.Provider == "mlx" {
		timeout = time.Hour
	} else if cfg.Provider == "apfel" {
		timeout = 30 * time.Second
	}

	if cfg.Provider == "hypura" {
		if err := llm.WaitForHypuraServer(cfg.BaseURL, timeout); err != nil {
			llm.StopServer(serverCmd)
			fmt.Fprintf(os.Stderr, "Server did not become ready: %v\n", err)
			os.Exit(1)
		}
		if servedModel, err := llm.QueryHypuraModel(cfg.BaseURL); err == nil {
			cfg.Model = servedModel
		}
	} else {
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
	}

	actualURL, err := llm.RegisterSharedServer(&llm.ServerInfo{
		Provider: cfg.Provider,
		Model:    cfg.Model,
		PID:      serverCmd.Process.Pid,
		Port:     port,
		BaseURL:  cfg.BaseURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error registering server: %v\n", err)
		os.Exit(1)
	}
	cfg.BaseURL = actualURL

	if verbose {
		fmt.Fprintln(os.Stderr, "Server ready")
	}
	return true
}

