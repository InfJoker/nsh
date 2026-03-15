package executor

import (
	"os/exec"

	tea "charm.land/bubbletea/v2"
	"github.com/anthropics/nsh/internal/msgs"
	"github.com/anthropics/nsh/internal/shell"
)

// LaunchInteractive creates a tea.ExecProcess command for TUI passthrough.
// The child process gets full terminal control (stdin/stdout/stderr).
func LaunchInteractive(command string, env *shell.EnvState, shellPath string) tea.Cmd {
	cmd := exec.Command(shellPath, "-c", command)
	cmd.Dir = env.GetCwd()
	cmd.Env = env.ToSlice()
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return msgs.InteractiveDoneMsg{Err: err}
	})
}
