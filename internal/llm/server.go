package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// LlamaCppInstalled checks if llama-server is on PATH.
func LlamaCppInstalled() bool {
	_, err := exec.LookPath("llama-server")
	return err == nil
}

// EnsureLlamaCpp installs llama.cpp if llama-server is not on PATH.
func EnsureLlamaCpp() error {
	if LlamaCppInstalled() {
		return nil
	}

	fmt.Println("  Installing llama.cpp...")
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("brew", "install", "llama.cpp")
	case "linux":
		cmd = exec.Command("brew", "install", "llama.cpp")
	default:
		return fmt.Errorf("llama.cpp auto-install not supported on %s — install manually: https://github.com/ggml-org/llama.cpp", runtime.GOOS)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("installing llama.cpp: %w", err)
	}
	fmt.Println("  ✓ llama.cpp installed")
	return nil
}

// FindFreePort binds to :0 and returns an OS-assigned ephemeral port.
func FindFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding free port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// StartLlamaServer starts `llama-server` with the given model and port.
// The model can be:
//   - A HuggingFace repo with optional quant (e.g. "Qwen/Qwen2.5-Coder-14B-Instruct-GGUF:Q4_K_M")
//     llama-server downloads automatically via --hf-repo
//   - A full .gguf file path → uses -m directly
//
// The caller is responsible for calling StopServer when done.
func StartLlamaServer(model string, port int) (*exec.Cmd, error) {
	args := []string{
		"--port", fmt.Sprintf("%d", port),
		"--host", "127.0.0.1",
	}

	if strings.HasSuffix(strings.Split(model, ":")[0], ".gguf") {
		// Direct .gguf path
		args = append(args, "-m", model)
	} else {
		// HuggingFace repo — llama-server auto-downloads
		args = append(args, "--hf-repo", model)
	}

	cmd := exec.Command("llama-server", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil // suppress server logs — they break TUI formatting
	// Create a new process group so StopServer kills llama-server and any children
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting llama-server: %w", err)
	}
	return cmd, nil
}

// StopServer kills a server process and its entire process group.
// Safe to call on already-exited processes.
func StopServer(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Try to kill the entire process group (negative PID).
	// Getpgid fails if the process already exited — that's fine.
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Process already exited, just reap it
		cmd.Wait()
		return
	}

	// SIGTERM the group for a clean shutdown
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	// Wait up to 3s for clean exit, then SIGKILL
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done // let the goroutine's Wait() reap
	}
}

// WaitForServer polls the OpenAI-compatible endpoint until it responds or timeout.
func WaitForServer(baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/models")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("server did not become ready within %v", timeout)
}

// QueryServedModel queries the server's /v1/models endpoint and returns
// the model ID it's actually serving. llama-server registers models under
// names that differ from the download identifier, so callers should use
// this name for API requests instead of the config/download name.
func QueryServedModel(baseURL string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/models")
	if err != nil {
		return "", fmt.Errorf("querying models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading models response: %w", err)
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing models response: %w", err)
	}
	if len(result.Data) == 0 {
		return "", fmt.Errorf("server reported no models")
	}
	return result.Data[0].ID, nil
}
