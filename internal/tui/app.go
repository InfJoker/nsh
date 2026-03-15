package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

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

	activeCmd *CommandModel

	busy           bool
	width, height  int
	cancelAgent    context.CancelFunc
	permPrompt     *permissionState
	execGuard      bool

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
		cfg:        cfg,
		env:        env,
		client:     client,
		history:    history,
		theme:      theme,
		shellPath:  cfg.Shell,
		projects:   projects,
		input:      input,
		stream:     stream,
		status:     status,
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
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

func (m Model) submitInput() (tea.Model, tea.Cmd) {
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
