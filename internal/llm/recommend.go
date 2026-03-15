package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// ModelRecommendation is a model suggestion from llmfit.
type ModelRecommendation struct {
	Name    string  `json:"name"`
	Size    string  `json:"size"`
	Score   float64 `json:"score"`
	Quality float64 `json:"quality"`
	Speed   float64 `json:"speed"`
}

// EnsureLlmfit checks for llmfit and auto-installs if missing.
func EnsureLlmfit() error {
	if _, err := exec.LookPath("llmfit"); err == nil {
		return nil
	}

	fmt.Println("  Installing llmfit for hardware-aware model recommendations...")
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("brew", "install", "llmfit")
	case "linux":
		cmd = exec.Command("cargo", "install", "llmfit")
	default:
		return fmt.Errorf("llmfit auto-install not supported on %s — install manually: https://github.com/seanmceligot/llmfit", runtime.GOOS)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("installing llmfit: %w", err)
	}
	fmt.Println("  ✓ llmfit installed")
	return nil
}

// RecommendModels uses llmfit to get hardware-scored model recommendations,
// then filters to only models that support tool calling in Ollama.
func RecommendModels(ollamaBaseURL string) ([]ModelRecommendation, error) {
	out, err := exec.Command("llmfit", "recommend", "--json", "--limit", "10").Output()
	if err != nil {
		return nil, fmt.Errorf("running llmfit: %w", err)
	}

	var recs []ModelRecommendation
	if err := json.Unmarshal(out, &recs); err != nil {
		return nil, fmt.Errorf("parsing llmfit output: %w", err)
	}

	// Filter to tool-calling capable models
	var filtered []ModelRecommendation
	for _, r := range recs {
		if ModelSupportsTools(ollamaBaseURL, r.Name) {
			filtered = append(filtered, r)
		}
	}

	return filtered, nil
}
