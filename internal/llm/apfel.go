package llm

import (
	"fmt"
	"os/exec"
	"syscall"
)

// ApfelInstalled checks if the apfel binary is on PATH.
func ApfelInstalled() bool {
	_, err := exec.LookPath("apfel")
	return err == nil
}

// StartApfelServer starts `apfel --serve` with the given port.
// apfel always serves a fixed model (apple-foundationmodel) — no model parameter needed.
// The caller is responsible for calling StopServer when done.
func StartApfelServer(port int) (*exec.Cmd, error) {
	cmd := exec.Command("apfel", "--serve",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting apfel: %w", err)
	}
	return cmd, nil
}
