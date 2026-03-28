package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"syscall"
	"time"
)

// HypuraInstalled checks if the hypura binary is on PATH.
func HypuraInstalled() bool {
	_, err := exec.LookPath("hypura")
	return err == nil
}

// StartHypuraServer starts `hypura serve` with the given GGUF model and port.
// The caller is responsible for calling StopServer when done.
func StartHypuraServer(model string, port int) (*exec.Cmd, error) {
	args := []string{
		"serve", model,
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
	}

	cmd := exec.Command("hypura", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting hypura: %w", err)
	}
	return cmd, nil
}

// WaitForHypuraServer polls the Ollama-compatible /api/tags endpoint until
// it responds or timeout. Cannot use WaitForServer which polls /v1/models.
func WaitForHypuraServer(baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/api/tags")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("hypura server did not become ready within %v", timeout)
}

// QueryHypuraModel queries the /api/tags endpoint and returns the model name
// that Hypura is serving.
func QueryHypuraModel(baseURL string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return "", fmt.Errorf("querying models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading tags response: %w", err)
	}

	// Ollama /api/tags format: {"models": [{"name": "...", ...}]}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing tags response: %w", err)
	}
	if len(result.Models) == 0 {
		return "", fmt.Errorf("server reported no models")
	}
	return result.Models[0].Name, nil
}
