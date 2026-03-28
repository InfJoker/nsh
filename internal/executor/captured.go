package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/InfJoker/nsh/internal/msgs"
	"github.com/InfJoker/nsh/internal/shell"
)

// DefaultTimeout is the default command timeout.
const DefaultTimeout = 30 * time.Second

// TimeoutFromSeconds converts seconds to a time.Duration.
func TimeoutFromSeconds(s int) time.Duration {
	return time.Duration(s) * time.Second
}

// PermissionFunc asks the TUI for permission to run a command.
// It blocks until the user responds or ctx is cancelled.
type PermissionFunc func(ctx context.Context, cmd string, isDangerous bool) (msgs.PermissionResponse, error)

// RunCaptured executes a command in the configured shell, streaming output.
// It checks built-ins first, then delegates to the shell.
func RunCaptured(
	ctx context.Context,
	command string,
	env *shell.EnvState,
	shellPath string,
	sendOutput func(msgs.CommandOutputMsg),
	timeout time.Duration,
) (exitCode int, err error) {
	// Check built-ins first
	result := shell.ExecBuiltin(env, command)
	if result.IsBuiltin {
		if result.Output != "" {
			sendOutput(msgs.CommandOutputMsg{Line: result.Output})
		}
		return result.ExitCode, nil
	}

	if timeout == 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shellPath, "-c", command)
	cmd.Dir = env.GetCwd()
	cmd.Env = env.ToSlice()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("creating stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return -1, fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("starting command: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	streamLines := func(r io.Reader, isStderr bool) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			sendOutput(msgs.CommandOutputMsg{
				Line:     scanner.Text(),
				IsStderr: isStderr,
			})
		}
	}

	go streamLines(stdoutPipe, false)
	go streamLines(stderrPipe, true)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}
