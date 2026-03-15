package llm

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const defaultOllamaBase = "http://localhost:11434"

// OllamaModel represents a model available in Ollama.
type OllamaModel struct {
	Name     string
	Size     int64 // bytes
	Modified time.Time
}

// DetectOllama checks if Ollama is running at the given base URL.
func DetectOllama(baseURL string) bool {
	if baseURL == "" {
		baseURL = defaultOllamaBase
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// ListOllamaModels returns models available in the local Ollama instance.
func ListOllamaModels(baseURL string) ([]OllamaModel, error) {
	if baseURL == "" {
		baseURL = defaultOllamaBase
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("connecting to Ollama: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name       string    `json:"name"`
			Size       int64     `json:"size"`
			ModifiedAt time.Time `json:"modified_at"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing Ollama response: %w", err)
	}

	var models []OllamaModel
	for _, m := range result.Models {
		models = append(models, OllamaModel{
			Name:     m.Name,
			Size:     m.Size,
			Modified: m.ModifiedAt,
		})
	}
	return models, nil
}

// ModelSupportsTools checks if a model supports tool/function calling.
func ModelSupportsTools(baseURL, model string) bool {
	if baseURL == "" {
		baseURL = defaultOllamaBase
	}
	client := &http.Client{Timeout: 5 * time.Second}

	body := fmt.Sprintf(`{"model":%q}`, model)
	resp, err := client.Post(baseURL+"/api/show", "application/json", strings.NewReader(body))
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var result struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false
	}

	for _, cap := range result.Capabilities {
		if cap == "tools" {
			return true
		}
	}
	return false
}

// OllamaInstalled checks if the ollama binary is on PATH.
func OllamaInstalled() bool {
	_, err := exec.LookPath("ollama")
	return err == nil
}

// InstallOllama runs the official install script (macOS/Linux only).
func InstallOllama() error {
	cmd := exec.Command("bash", "-c", "curl -fsSL https://ollama.com/install.sh | sh")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// StartOllama launches ollama serve in the background and waits for it to be ready.
func StartOllama() error {
	cmd := exec.Command("ollama", "serve")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting ollama: %w", err)
	}

	// Wait for port 11434 to be ready
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "localhost:11434", 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("ollama did not start within 15 seconds")
}

// PullModel runs ollama pull with progress output.
func PullModel(model string) error {
	cmd := exec.Command("ollama", "pull", model)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
