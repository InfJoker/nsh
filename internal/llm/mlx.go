package llm

import (
	"fmt"
	"os"
	"os/exec"
)

// mlxServerCommand returns the command and args for running mlx_lm.server.
// Prefers the standalone `mlx_lm.server` binary (installed via uv tool or pip),
// falls back to `python3 -m mlx_lm server`.
func mlxServerCommand() (string, []string) {
	if path, err := exec.LookPath("mlx_lm.server"); err == nil {
		return path, nil
	}
	return "python3", []string{"-m", "mlx_lm", "server"}
}

// MlxLmInstalled checks if mlx_lm.server is available.
func MlxLmInstalled() bool {
	bin, baseArgs := mlxServerCommand()
	cmd := exec.Command(bin, append(baseArgs, "--help")...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// EnsureMlxLm installs mlx-lm if not already available.
// Prefers `uv tool install` (isolated env, latest Python) over `pip3 install`.
func EnsureMlxLm() error {
	if MlxLmInstalled() {
		return nil
	}

	fmt.Println("  Installing mlx-lm...")
	var cmd *exec.Cmd
	if _, err := exec.LookPath("uv"); err == nil {
		cmd = exec.Command("uv", "tool", "install", "mlx-lm")
	} else {
		cmd = exec.Command("pip3", "install", "mlx-lm")
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("installing mlx-lm: %w", err)
	}
	fmt.Println("  ✓ mlx-lm installed")
	return nil
}

// StartMlxServer starts mlx_lm.server with the given model and port
// and returns the process handle. The caller is responsible for calling StopServer when done.
// mlx_lm.server auto-downloads models from HuggingFace on first use.
func StartMlxServer(model string, port int) (*exec.Cmd, error) {
	bin, baseArgs := mlxServerCommand()
	args := append(baseArgs, "--model", model, "--port", fmt.Sprintf("%d", port))
	cmd := exec.Command(bin, args...)
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr // show download progress and server logs
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting mlx_lm server: %w", err)
	}
	return cmd, nil
}
