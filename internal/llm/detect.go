package llm

import (
	"net/http"
	"os"
	"time"
)

// ProviderInfo describes an available LLM provider.
type ProviderInfo struct {
	Name      string
	Available bool
	Hint      string // setup hint when not available (e.g. "export ANTHROPIC_API_KEY=...")
	Model     string
	BaseURL   string
}

// DetectAvailableProviders returns the list of known providers with availability status.
func DetectAvailableProviders() []ProviderInfo {
	anthropicAvail := os.Getenv("ANTHROPIC_API_KEY") != ""
	ollamaAvail := ollamaReachable()

	llamaCppAvail := LlamaCppInstalled()
	mlxAvail := MlxLmInstalled()

	providers := []ProviderInfo{
		{
			Name:      "anthropic",
			Available: anthropicAvail,
			Hint:      hintwhen(!anthropicAvail, "export ANTHROPIC_API_KEY=sk-..."),
			Model:     "claude-sonnet-4-20250514",
		},
		{
			Name:      "ollama",
			Available: ollamaAvail,
			Hint:      hintwhen(!ollamaAvail, "curl -fsSL https://ollama.com/install.sh | sh"),
			Model:     "", // determined during interactive setup
			BaseURL:   "http://localhost:11434/v1",
		},
		{
			Name:      "llama.cpp",
			Available: llamaCppAvail,
			Hint:      hintwhen(!llamaCppAvail, "brew install llama.cpp"),
			Model:     "", // determined during interactive setup
		},
		{
			Name:      "mlx",
			Available: mlxAvail,
			Hint:      hintwhen(!mlxAvail, "pip3 install mlx-lm"),
			Model:     "", // determined during interactive setup
		},
		{
			Name:      "mock",
			Available: true,
			Model:     "mock",
		},
	}
	return providers
}

func hintwhen(cond bool, hint string) string {
	if cond {
		return hint
	}
	return ""
}

// ollamaReachable checks if Ollama is running by pinging its API.
func ollamaReachable() bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
