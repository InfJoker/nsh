package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// ResolveCachedModel looks up the llmfit cache directory for a .gguf file
// matching the given model identifier (HuggingFace repo name or base name).
// Returns the cache filename without extension (suitable for `llmfit run`),
// or empty string if not found.
func ResolveCachedModel(model string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	cacheDir := filepath.Join(home, ".cache", "llmfit", "models")
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return ""
	}
	// Extract the model's base name from the repo path (e.g. "hf.co/unsloth/Qwen3.5-9B-GGUF" → "Qwen3.5-9B")
	modelBase := model
	if idx := strings.LastIndex(modelBase, "/"); idx >= 0 {
		modelBase = modelBase[idx+1:]
	}
	modelBase = strings.TrimSuffix(modelBase, "-GGUF")
	modelBase = strings.ToLower(modelBase)

	for _, e := range entries {
		name := e.Name()
		nameLower := strings.ToLower(name)
		if strings.HasPrefix(nameLower, modelBase) && strings.HasSuffix(nameLower, ".gguf") && !strings.HasSuffix(nameLower, ".part") {
			return strings.TrimSuffix(name, ".gguf")
		}
	}
	return ""
}

// ModelCached checks if a model is already in llmfit's local cache.
func ModelCached(model string) bool {
	return ResolveCachedModel(model) != ""
}

// DownloadModel wraps `llmfit download <model>` to fetch a model with auto quant selection.
// Skips the download if the model is already in the local cache.
func DownloadModel(model string) error {
	if ModelCached(model) {
		fmt.Printf("  ✓ %s already cached, skipping download\n", model)
		return nil
	}
	cmd := exec.Command("llmfit", "download", model)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

// StartLlmfitServer starts `llmfit run <model> --server --port <port>` and returns
// the process handle. The caller is responsible for calling StopServer when done.
// The model can be a HuggingFace repo name (e.g. "hf.co/unsloth/Qwen3.5-9B-GGUF")
// or a cached model name — the function resolves to the cached name automatically.
func StartLlmfitServer(model string, port int) (*exec.Cmd, error) {
	// Resolve HuggingFace repo names to cached model names
	if cached := ResolveCachedModel(model); cached != "" {
		model = cached
	}
	cmd := exec.Command("llmfit", "run", model, "--server", "--port", fmt.Sprintf("%d", port))
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting llmfit server: %w", err)
	}
	return cmd, nil
}

// StopServer kills the llmfit server process.
func StopServer(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	cmd.Process.Kill()
	cmd.Wait()
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
	return fmt.Errorf("llmfit server did not become ready within %v", timeout)
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
