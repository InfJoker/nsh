package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/InfJoker/nsh/internal/agent"
	"github.com/InfJoker/nsh/internal/config"
	"github.com/InfJoker/nsh/internal/executor"
	"github.com/InfJoker/nsh/internal/llm"
	"github.com/InfJoker/nsh/internal/msgs"
	"github.com/InfJoker/nsh/internal/shell"
)

// ProgramRef is a shared holder for a *tea.Program reference.
// It is shared between the original Model value in main.go and the copy
// inside bubbletea, so SetProgram works regardless of which copy is used.
type ProgramRef struct {
	p atomic.Pointer[tea.Program]
}

// Set stores the program reference.
func (r *ProgramRef) Set(p *tea.Program) {
	r.p.Store(p)
}

// Get returns the program reference, or nil if not yet set.
func (r *ProgramRef) Get() *tea.Program {
	return r.p.Load()
}


// conversationEntry is a rendered block in the conversation history.
type conversationEntry struct {
	content string
}

// permissionState tracks an active permission prompt.
type permissionState struct {
	command    string
	dangerous  bool
	responseCh chan<- msgs.PermissionResponse
}

// localServerReadyMsg signals a local server (llama.cpp or MLX) is ready (or failed).
type localServerReadyMsg struct {
	Provider string
	Model    string
	BaseURL  string
	Err      error
}

// Model is the root bubbletea model.
type Model struct {
	cfg       *config.Config
	env       *shell.EnvState
	client    llm.LLMClient
	history   *agent.History
	theme     Theme
	shellPath string
	projects  *shell.ProjectIndex

	input  InputModel
	stream *StreamModel
	status StatusModel

	entries []conversationEntry

	activeCmd      *CommandModel
	providerSelect *providerSelectState
	presetSelect   *presetSelectState

	busy            bool
	width, height   int
	cancelAgent     context.CancelFunc
	permPrompt      *permissionState
	execGuard       bool
	serverStarting  bool

	// programRef is a shared pointer holder so both the main.go copy
	// and bubbletea's internal copy of Model reference the same *tea.Program.
	programRef *ProgramRef
}

// NewApp creates the root model.
func NewApp(
	cfg *config.Config,
	env *shell.EnvState,
	client llm.LLMClient,
	history *agent.History,
	projects *shell.ProjectIndex,
) Model {
	theme := GetTheme(cfg.Theme)
	input := NewInputModel(theme)
	stream := NewStreamModel(theme)
	status := NewStatusModel(theme)
	status.SetCwd(env.GetCwd())
	status.SetModel(cfg.Model)

	input.SetHistory(history.Inputs())

	return Model{
		cfg:         cfg,
		env:         env,
		client:      client,
		history:     history,
		theme:       theme,
		shellPath:   cfg.Shell,
		projects:    projects,
		input:       input,
		stream:      stream,
		status:      status,
		programRef: &ProgramRef{}, // shared pointer, survives value copies
	}
}

// SetProgram stores the program reference. Safe to call on any copy of Model
// because programRef is a shared pointer allocated in NewApp.
func (m *Model) SetProgram(p *tea.Program) {
	m.programRef.Set(p)
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(m.width)
		m.status.SetWidth(m.width)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	// Agent messages (arrive via program.Send from agent goroutine)
	case msgs.TokenMsg:
		m.stream.AddToken(msg.Text)
		return m, nil

	case msgs.StreamDoneMsg:
		m.stream.Done()
		if !m.stream.IsEmpty() {
			m.entries = append(m.entries, conversationEntry{content: m.stream.View()})
			m.stream.Reset()
		}
		return m, nil

	case msgs.ToolCallStartMsg:
		if msg.Name == "launch_interactive" {
			m.execGuard = true
			return m, executor.LaunchInteractive(msg.Desc, m.env, m.shellPath)
		}
		isDangerous := false
		if msg.Name == "run_command" {
			isDangerous = m.cfg.IsDangerous(msg.Desc)
		}
		cmd := NewCommandModel(msg.Desc, isDangerous, m.theme)
		m.activeCmd = &cmd
		return m, nil

	case msgs.ToolCallDoneMsg:
		if m.activeCmd != nil {
			m.activeCmd.Done()
			m.entries = append(m.entries, conversationEntry{content: m.activeCmd.View()})
			m.activeCmd = nil
		}
		return m, nil

	case msgs.CommandOutputMsg:
		if m.activeCmd != nil {
			m.activeCmd.AddOutput(msg.Line, msg.IsStderr)
		}
		return m, nil

	case msgs.AgentErrorMsg:
		errStyle := lipgloss.NewStyle().
			Foreground(m.theme.Danger).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Danger).
			Padding(0, 1)
		m.entries = append(m.entries, conversationEntry{
			content: errStyle.Render(fmt.Sprintf("Error: %v", msg.Err)),
		})
		return m, nil

	case msgs.AgentDoneMsg:
		m.busy = false
		m.status.SetBusy(false)
		m.input.SetFocused(true)
		m.input.SetHistory(m.history.Inputs())
		m.cancelAgent = nil
		return m, nil

	case msgs.CwdChangedMsg:
		m.status.SetCwd(msg.Path)
		return m, nil

	case msgs.PermissionRequestMsg:
		m.permPrompt = &permissionState{
			command:    msg.Command,
			dangerous:  msg.IsDangerous,
			responseCh: msg.ResponseCh,
		}
		return m, nil

	case msgs.InteractiveDoneMsg:
		m.execGuard = false
		if msg.Err != nil {
			errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
			m.entries = append(m.entries, conversationEntry{
				content: errStyle.Render(fmt.Sprintf("Error: %v", msg.Err)),
			})
		}
		return m, nil

	case msgs.OllamaSetupDoneMsg:
		m.execGuard = false
		if msg.Err != nil {
			errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
			m.entries = append(m.entries, conversationEntry{
				content: errStyle.Render(fmt.Sprintf("Ollama setup failed: %v", msg.Err)),
			})
			return m, nil
		}
		return m.applyProviderSwitch("ollama", msg.Model, msg.BaseURL)

	case msgs.LlamaCppSetupDoneMsg:
		m.execGuard = false
		if msg.Err != nil {
			errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
			m.entries = append(m.entries, conversationEntry{
				content: errStyle.Render(fmt.Sprintf("llama.cpp setup failed: %v", msg.Err)),
			})
			return m, nil
		}
		// Start or reuse server asynchronously via tea.Cmd
		model := msg.Model
		infoStyle := lipgloss.NewStyle().Foreground(m.theme.Muted)
		m.entries = append(m.entries, conversationEntry{
			content: infoStyle.Render("Starting llama-server..."),
		})
		m.serverStarting = true
		return m, func() tea.Msg {
			return acquireOrStartServer("llama.cpp", model)
		}

	case msgs.MLXSetupDoneMsg:
		m.execGuard = false
		if msg.Err != nil {
			errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
			m.entries = append(m.entries, conversationEntry{
				content: errStyle.Render(fmt.Sprintf("MLX setup failed: %v", msg.Err)),
			})
			return m, nil
		}
		// Start or reuse server asynchronously via tea.Cmd
		model := msg.Model
		infoStyle := lipgloss.NewStyle().Foreground(m.theme.Muted)
		m.entries = append(m.entries, conversationEntry{
			content: infoStyle.Render("Starting MLX server (may download model on first use)..."),
		})
		m.serverStarting = true
		return m, func() tea.Msg {
			return acquireOrStartServer("mlx", model)
		}

	case msgs.HypuraSetupDoneMsg:
		m.execGuard = false
		if msg.Err != nil {
			errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
			m.entries = append(m.entries, conversationEntry{
				content: errStyle.Render(fmt.Sprintf("Hypura setup failed: %v", msg.Err)),
			})
			return m, nil
		}
		model := msg.Model
		infoStyle := lipgloss.NewStyle().Foreground(m.theme.Muted)
		m.entries = append(m.entries, conversationEntry{
			content: infoStyle.Render("Starting Hypura server..."),
		})
		m.serverStarting = true
		return m, func() tea.Msg {
			return acquireOrStartServer("hypura", model)
		}

	case localServerReadyMsg:
		m.serverStarting = false
		if msg.Err != nil {
			errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
			m.entries = append(m.entries, conversationEntry{
				content: errStyle.Render(fmt.Sprintf("%s server failed: %v", msg.Provider, msg.Err)),
			})
			return m, nil
		}
		return m.applyProviderSwitch(msg.Provider, msg.Model, msg.BaseURL)
	}

	return m, nil
}

// acquireOrStartServer handles the shared server lifecycle for provider switches.
// Releases the previous server, then acquires or starts a new one.
// Runs in a tea.Cmd goroutine (not on the main event loop).
func acquireOrStartServer(provider, model string) localServerReadyMsg {
	// Release previous shared server (if any)
	llm.ReleaseSharedServer()

	// Try to reuse an existing server
	baseURL, err := llm.AcquireSharedServer(provider, model)
	if err != nil {
		return localServerReadyMsg{Provider: provider, Err: err}
	}
	if baseURL != "" {
		return localServerReadyMsg{Provider: provider, Model: model, BaseURL: baseURL}
	}

	// Start a new server
	port, err := llm.FindFreePort()
	if err != nil {
		return localServerReadyMsg{Provider: provider, Err: fmt.Errorf("finding free port: %w", err)}
	}

	// Hypura uses Ollama-native API (no /v1 prefix)
	var srvBaseURL string
	if provider == "hypura" {
		srvBaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	} else {
		srvBaseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", port)
	}

	var serverCmd *exec.Cmd
	switch provider {
	case "mlx":
		serverCmd, err = llm.StartMlxServer(model, port)
	case "hypura":
		serverCmd, err = llm.StartHypuraServer(model, port)
	default:
		serverCmd, err = llm.StartLlamaServer(model, port)
	}
	if err != nil {
		return localServerReadyMsg{Provider: provider, Err: fmt.Errorf("starting server: %w", err)}
	}

	timeout := 30 * time.Minute
	if provider == "mlx" {
		timeout = time.Hour
	}

	if provider == "hypura" {
		if err := llm.WaitForHypuraServer(srvBaseURL, timeout); err != nil {
			llm.StopServer(serverCmd)
			return localServerReadyMsg{Provider: provider, Err: fmt.Errorf("server not ready: %w", err)}
		}
	} else {
		if err := llm.WaitForServer(srvBaseURL, timeout); err != nil {
			llm.StopServer(serverCmd)
			return localServerReadyMsg{Provider: provider, Err: fmt.Errorf("server not ready: %w", err)}
		}
	}

	servedModel := model
	switch provider {
	case "llama.cpp":
		if name, err := llm.QueryServedModel(srvBaseURL); err == nil {
			servedModel = name
		}
	case "hypura":
		if name, err := llm.QueryHypuraModel(srvBaseURL); err == nil {
			servedModel = name
		}
	}

	actualURL, regErr := llm.RegisterSharedServer(&llm.ServerInfo{
		Provider: provider,
		Model:    servedModel,
		PID:      serverCmd.Process.Pid,
		Port:     port,
		BaseURL:  srvBaseURL,
	})
	if regErr != nil {
		return localServerReadyMsg{Provider: provider, Err: regErr}
	}

	return localServerReadyMsg{Provider: provider, Model: servedModel, BaseURL: actualURL}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.presetSelect != nil {
		return m.handlePresetSelectKey(msg)
	}
	if m.providerSelect != nil {
		return m.handleProviderSelectKey(msg)
	}

	if m.permPrompt != nil {
		return m.handlePermissionKey(msg)
	}

	key := msg.Key()

	// Ctrl+C
	if key.Code == 'c' && key.Mod&tea.ModCtrl != 0 {
		if m.busy && m.cancelAgent != nil {
			m.cancelAgent()
			return m, nil
		}
		return m, tea.Quit
	}

	// Ctrl+D
	if key.Code == 'd' && key.Mod&tea.ModCtrl != 0 {
		return m, tea.Quit
	}

	if m.busy {
		return m, nil
	}

	// Enter
	if key.Code == tea.KeyEnter {
		return m.submitInput()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m Model) handlePermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	pp := m.permPrompt

	switch {
	case key.Code == tea.KeyEnter:
		pp.responseCh <- msgs.PermissionOnce
		m.permPrompt = nil
	case key.Code == 'a' && key.Mod == 0:
		if !pp.dangerous {
			pp.responseCh <- msgs.PermissionAlways
		} else {
			pp.responseCh <- msgs.PermissionOnce
		}
		m.permPrompt = nil
	case key.Code == tea.KeyEscape || (key.Code == 'c' && key.Mod&tea.ModCtrl != 0):
		pp.responseCh <- msgs.PermissionDeny
		m.permPrompt = nil
	}

	return m, nil
}

func (m Model) handlePresetSelectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ps := m.presetSelect
	key := msg.Key()

	switch {
	case key.Code == tea.KeyUp:
		if ps.cursor > 0 {
			ps.cursor--
		}
	case key.Code == tea.KeyDown:
		if ps.cursor < len(ps.entries)-1 {
			ps.cursor++
		}
	case key.Code == tea.KeyEnter:
		name := ps.selected()
		m.presetSelect = nil
		if name != "" {
			return m.switchToPreset(name)
		}
	case key.Code == tea.KeyEscape || (key.Code == 'c' && key.Mod&tea.ModCtrl != 0):
		m.presetSelect = nil
	}

	return m, nil
}

// switchToPreset switches the active preset. Handles server lifecycle.
func (m Model) switchToPreset(name string) (tea.Model, tea.Cmd) {
	preset, prov, err := m.cfg.ResolvePreset(name)
	if err != nil {
		errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
		m.entries = append(m.entries, conversationEntry{
			content: errStyle.Render(fmt.Sprintf("Preset error: %v", err)),
		})
		return m, nil
	}

	provType := prov.Type
	model := preset.Model
	baseURL := prov.BaseURL

	// For local providers, need to start/acquire server
	if provType == "mlx" || provType == "llama.cpp" || provType == "hypura" {
		infoStyle := lipgloss.NewStyle().Foreground(m.theme.Muted)
		m.entries = append(m.entries, conversationEntry{
			content: infoStyle.Render(fmt.Sprintf("Switching to %s (%s/%s)...", name, provType, model)),
		})
		m.serverStarting = true
		return m, func() tea.Msg {
			return acquireOrStartServer(provType, model)
		}
	}

	// For cloud/mock providers, switch immediately
	return m.applyProviderSwitch(provType, model, baseURL)
}

func (m Model) handleProviderSelectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ps := m.providerSelect
	key := msg.Key()

	switch {
	case key.Code == tea.KeyUp:
		if ps.cursor > 0 {
			ps.cursor--
		}
	case key.Code == tea.KeyDown:
		if ps.cursor < len(ps.providers)-1 {
			ps.cursor++
		}
	case key.Code == tea.KeyEnter:
		sel := ps.selected()
		if sel == nil {
			return m, nil
		}
		// Ollama always gets the interactive setup (install + model selection)
		if sel.Name == "ollama" {
			m.providerSelect = nil
			m.execGuard = true
			return m, launchOllamaSetup()
		}
		// llama.cpp always gets the interactive setup (model selection)
		if sel.Name == "llama.cpp" {
			m.providerSelect = nil
			m.execGuard = true
			return m, launchLlamaCppSetup()
		}
		// MLX always gets the interactive setup (mlx-lm + model selection)
		if sel.Name == "mlx" {
			m.providerSelect = nil
			m.execGuard = true
			return m, launchMLXSetup()
		}
		// Hypura always gets the interactive setup (GGUF model path)
		if sel.Name == "hypura" {
			m.providerSelect = nil
			m.execGuard = true
			return m, launchHypuraSetup()
		}
		if !sel.Available {
			return m, nil
		}
		return m.applyProviderSwitch(sel.Name, sel.Model, sel.BaseURL)
	case key.Code == tea.KeyEscape || (key.Code == 'c' && key.Mod&tea.ModCtrl != 0):
		m.providerSelect = nil
	}

	return m, nil
}

func (m Model) applyProviderSwitch(name, model, baseURL string) (tea.Model, tea.Cmd) {
	newClient, err := llm.NewProvider(name, model, baseURL)
	if err != nil {
		errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
		m.entries = append(m.entries, conversationEntry{
			content: errStyle.Render(fmt.Sprintf("Failed to switch provider: %v", err)),
		})
		m.providerSelect = nil
		return m, nil
	}
	// Release previous shared server if switching to a non-local provider
	if name != "llama.cpp" && name != "mlx" && name != "hypura" {
		llm.ReleaseSharedServer()
	}
	m.client = newClient
	m.cfg.SaveProviderFull(name, model, baseURL)
	m.status.SetModel(model)
	m.history.Clear()
	m.entries = nil
	m.stream.Reset()

	okStyle := lipgloss.NewStyle().Foreground(m.theme.Success)
	m.entries = append(m.entries, conversationEntry{
		content: okStyle.Render(fmt.Sprintf("Switched to %s (%s)", name, model)),
	})
	m.providerSelect = nil
	return m, nil
}

// launchOllamaSetup runs the ollama setup flow as a subprocess via tea.ExecProcess.
func launchOllamaSetup() tea.Cmd {
	resultFile := filepath.Join(os.TempDir(), fmt.Sprintf("nsh-ollama-setup-%d", os.Getpid()))
	self, _ := os.Executable()
	c := exec.Command(self, "--ollama-setup", resultFile)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(resultFile)
		if err != nil {
			return msgs.OllamaSetupDoneMsg{Err: err}
		}
		data, err := os.ReadFile(resultFile)
		if err != nil {
			return msgs.OllamaSetupDoneMsg{Err: fmt.Errorf("reading setup result: %w", err)}
		}
		lines := strings.SplitN(string(data), "\n", 3)
		if len(lines) < 2 {
			return msgs.OllamaSetupDoneMsg{Err: fmt.Errorf("unexpected setup result")}
		}
		return msgs.OllamaSetupDoneMsg{Model: lines[0], BaseURL: lines[1]}
	})
}

// launchLlamaCppSetup runs the llama.cpp setup flow as a subprocess via tea.ExecProcess.
// The subprocess only handles model selection/download. Port allocation and server startup
// happen in the parent process to avoid TOCTOU races.
func launchLlamaCppSetup() tea.Cmd {
	resultFile := filepath.Join(os.TempDir(), fmt.Sprintf("nsh-llamacpp-setup-%d", os.Getpid()))
	self, _ := os.Executable()
	c := exec.Command(self, "--llamacpp-setup", resultFile)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(resultFile)
		if err != nil {
			return msgs.LlamaCppSetupDoneMsg{Err: err}
		}
		data, err := os.ReadFile(resultFile)
		if err != nil {
			return msgs.LlamaCppSetupDoneMsg{Err: fmt.Errorf("reading setup result: %w", err)}
		}
		model := strings.TrimSpace(string(data))
		if model == "" {
			return msgs.LlamaCppSetupDoneMsg{Err: fmt.Errorf("no model selected")}
		}
		return msgs.LlamaCppSetupDoneMsg{Model: model}
	})
}

// launchMLXSetup runs the MLX setup flow as a subprocess via tea.ExecProcess.
// The subprocess only handles model selection. Port allocation and server startup
// happen in the parent process.
func launchMLXSetup() tea.Cmd {
	resultFile := filepath.Join(os.TempDir(), fmt.Sprintf("nsh-mlx-setup-%d", os.Getpid()))
	self, _ := os.Executable()
	c := exec.Command(self, "--mlx-setup", resultFile)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(resultFile)
		if err != nil {
			return msgs.MLXSetupDoneMsg{Err: err}
		}
		data, err := os.ReadFile(resultFile)
		if err != nil {
			return msgs.MLXSetupDoneMsg{Err: fmt.Errorf("reading setup result: %w", err)}
		}
		model := strings.TrimSpace(string(data))
		if model == "" {
			return msgs.MLXSetupDoneMsg{Err: fmt.Errorf("no model selected")}
		}
		return msgs.MLXSetupDoneMsg{Model: model}
	})
}

// launchHypuraSetup runs the Hypura setup flow as a subprocess via tea.ExecProcess.
// The subprocess only handles model path selection. Port allocation and server startup
// happen in the parent process.
func launchHypuraSetup() tea.Cmd {
	resultFile := filepath.Join(os.TempDir(), fmt.Sprintf("nsh-hypura-setup-%d", os.Getpid()))
	self, _ := os.Executable()
	c := exec.Command(self, "--hypura-setup", resultFile)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(resultFile)
		if err != nil {
			return msgs.HypuraSetupDoneMsg{Err: err}
		}
		data, err := os.ReadFile(resultFile)
		if err != nil {
			return msgs.HypuraSetupDoneMsg{Err: fmt.Errorf("reading setup result: %w", err)}
		}
		model := strings.TrimSpace(string(data))
		if model == "" {
			return msgs.HypuraSetupDoneMsg{Err: fmt.Errorf("no model selected")}
		}
		return msgs.HypuraSetupDoneMsg{Model: model}
	})
}

func (m Model) submitInput() (tea.Model, tea.Cmd) {
	if m.serverStarting {
		return m, nil
	}
	input := strings.TrimSpace(m.input.Value())
	if input == "" {
		return m, nil
	}

	userStyle := lipgloss.NewStyle().Foreground(m.theme.Primary).Bold(true)
	m.entries = append(m.entries, conversationEntry{
		content: userStyle.Render("❯ ") + input,
	})

	m.input.Reset()

	dispatch := shell.Dispatch(input)

	switch dispatch.Type {
	case shell.InputBuiltin:
		result := shell.ExecBuiltin(m.env, dispatch.Command)
		if result.IsProviderSwitch {
			m.providerSelect = newProviderSelectState()
			return m, nil
		}
		if result.IsPresetSwitch {
			if result.PresetArg != "" {
				// Direct switch: !presets light
				return m.switchToPreset(result.PresetArg)
			}
			m.presetSelect = newPresetSelectState(m.cfg)
			if m.presetSelect == nil {
				m.entries = append(m.entries, conversationEntry{content: "No presets configured. Add one in ~/.nsh/config.toml"})
			}
			return m, nil
		}
		if result.Output != "" {
			m.entries = append(m.entries, conversationEntry{content: result.Output})
		}
		if result.NewCwd != "" {
			m.status.SetCwd(result.NewCwd)
			if m.projects != nil {
				m.projects.Record(result.NewCwd)
			}
		}
		return m, nil

	case shell.InputDirect:
		return m.runDirectCommand(dispatch.Command)

	case shell.InputNL:
		return m.startAgent(input)
	}

	return m, nil
}

// runDirectCommand runs a ! command via tea.ExecProcess (full terminal passthrough).
// The user explicitly asked for direct execution — give them the real terminal.
// No interactive detection needed: tea.ExecProcess works for everything (ls, vim, ssh, etc.).
func (m Model) runDirectCommand(command string) (tea.Model, tea.Cmd) {
	// Check permissions before executing
	perm := executor.EvaluatePermission(command, m.cfg)
	if perm.Action == config.ActionDeny {
		errStyle := lipgloss.NewStyle().Foreground(m.theme.Danger)
		m.entries = append(m.entries, conversationEntry{
			content: errStyle.Render("Command denied by permission rules: " + command),
		})
		return m, nil
	}

	m.execGuard = true
	return m, executor.LaunchInteractive(command, m.env, m.shellPath)
}


func (m Model) startAgent(input string) (tea.Model, tea.Cmd) {
	m.busy = true
	m.status.SetBusy(true)
	m.input.SetFocused(false)
	m.stream.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelAgent = cancel

	client := m.client
	history := m.history
	env := m.env
	cfg := m.cfg
	shellPath := m.shellPath
	pRef := m.programRef // shared pointer — survives value copy
	projects := m.projects

	// sendMsg uses program.Send to deliver intermediate messages to the TUI
	sendMsg := func(msg any) {
		if p := pRef.Get(); p != nil {
			p.Send(msg)
		}
	}

	// permFn bridges the agent's permission requests to the TUI via program.Send
	permFn := func(ctx context.Context, cmd string, isDangerous bool) (msgs.PermissionResponse, error) {
		p := pRef.Get()
		if p == nil {
			return msgs.PermissionOnce, nil
		}
		// Buffered channel prevents deadlock if context cancelled before TUI responds
		ch := make(chan msgs.PermissionResponse, 1)
		p.Send(msgs.PermissionRequestMsg{
			Command:     cmd,
			IsDangerous: isDangerous,
			ResponseCh:  ch,
		})
		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			return msgs.PermissionDeny, ctx.Err()
		}
	}

	return m, func() tea.Msg {
		a := agent.NewAgent(
			client,
			history,
			env,
			cfg,
			shellPath,
			sendMsg,
			permFn,
		)
		a.SetProjects(projects)
		a.Run(ctx, input)
		cancel()
		// AgentDoneMsg is NOT returned here — it is sent via sendMsg
		// from the deferred func in Agent.Run(). Returning nil avoids double delivery.
		return nil
	}
}

func (m Model) View() tea.View {
	if m.execGuard {
		return tea.NewView("")
	}

	var sb strings.Builder

	for _, entry := range m.entries {
		sb.WriteString(entry.content)
		sb.WriteString("\n")
	}

	if !m.stream.IsEmpty() {
		sb.WriteString(m.stream.View())
		sb.WriteString("\n")
	}

	if m.activeCmd != nil {
		sb.WriteString(m.activeCmd.View())
	}

	if m.permPrompt != nil {
		sb.WriteString(m.renderPermissionPrompt())
	}

	if m.providerSelect != nil {
		sb.WriteString(renderProviderSelect(m.providerSelect, m.theme))
	}

	if m.presetSelect != nil {
		sb.WriteString(renderPresetSelect(m.presetSelect, m.theme))
	}

	if !m.busy || m.permPrompt != nil {
		sb.WriteString(m.input.View())
	} else {
		spinner := lipgloss.NewStyle().Foreground(m.theme.Primary).Render("⠋ thinking...")
		sb.WriteString(spinner)
	}

	sb.WriteString("\n")
	sb.WriteString(m.status.View())

	return tea.NewView(sb.String())
}

func (m Model) renderPermissionPrompt() string {
	pp := m.permPrompt
	if pp == nil {
		return ""
	}

	cmdStyle := lipgloss.NewStyle().Foreground(m.theme.Secondary).Bold(true)
	if pp.dangerous {
		cmdStyle = lipgloss.NewStyle().Foreground(m.theme.Danger).Bold(true)
	}

	var sb strings.Builder
	sb.WriteString(cmdStyle.Render("$ " + pp.command))
	sb.WriteString("\n")

	if pp.dangerous {
		warn := lipgloss.NewStyle().Foreground(m.theme.Danger).Render("  ⚠ DANGEROUS COMMAND")
		sb.WriteString(warn + "\n")
	}

	hint := lipgloss.NewStyle().Foreground(m.theme.Muted)
	if pp.dangerous {
		sb.WriteString(hint.Render("  [Enter] Run  [Esc] Cancel"))
	} else {
		sb.WriteString(hint.Render("  [Enter] Run  [a] Always  [Esc] Cancel"))
	}
	sb.WriteString("\n")

	return sb.String()
}
