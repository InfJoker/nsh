package llm

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// OllamaSetupResult holds the result of the interactive Ollama setup flow.
type OllamaSetupResult struct {
	Model   string
	BaseURL string
}

// LlamaCppSetupResult holds the result of the interactive llama.cpp setup flow.
type LlamaCppSetupResult struct {
	Model string
}

// RunOllamaSetup handles the interactive Ollama install/model selection flow.
// It only shows locally-installed tool-capable models — no recommendations or downloads.
// It reads from stdin and writes to stdout — must be called outside the TUI
// (e.g. via tea.Exec or before the TUI starts).
func RunOllamaSetup() (*OllamaSetupResult, error) {
	reader := bufio.NewReader(os.Stdin)
	baseURL := "http://localhost:11434/v1"
	ollamaBase := "http://localhost:11434"

	// Step 1: Ensure Ollama is installed and running
	if !DetectOllama("") {
		if !OllamaInstalled() {
			if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
				return nil, fmt.Errorf("ollama is not available on this platform — install manually: https://ollama.com/download")
			}

			fmt.Println()
			fmt.Println("  Ollama not found.")
			fmt.Println("  Install now? This will run: curl -fsSL https://ollama.com/install.sh | sh")
			fmt.Printf("  [Y/n]: ")
			ans, _ := reader.ReadString('\n')
			ans = strings.TrimSpace(strings.ToLower(ans))
			if ans != "" && ans != "y" && ans != "yes" {
				return nil, fmt.Errorf("ollama installation skipped")
			}

			fmt.Println()
			fmt.Println("  Installing Ollama...")
			if err := InstallOllama(); err != nil {
				return nil, fmt.Errorf("installing Ollama: %w", err)
			}
			fmt.Println("  ✓ Ollama installed")
		}

		// Ollama installed but not running
		fmt.Println("  Starting Ollama...")
		if err := StartOllama(); err != nil {
			return nil, fmt.Errorf("starting Ollama: %w (try: ollama serve)", err)
		}
		fmt.Println("  ✓ Ollama ready")
	}

	// Step 2: List models and filter by tool-calling capability
	var models []OllamaModel
	var listErr error
	for attempts := 0; attempts < 3; attempts++ {
		models, listErr = ListOllamaModels(ollamaBase)
		if listErr == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if listErr != nil {
		return nil, fmt.Errorf("listing models: %w", listErr)
	}

	type modelEntry struct {
		name string
		size int64
	}
	var toolCapable []modelEntry
	for _, m := range models {
		if ModelSupportsTools(ollamaBase, m.Name) {
			toolCapable = append(toolCapable, modelEntry{name: m.Name, size: m.Size})
		}
	}

	// Step 3: If no tool-capable models, tell user to pull manually
	if len(toolCapable) == 0 {
		fmt.Println()
		if len(models) == 0 {
			fmt.Println("  No models installed in Ollama.")
		} else {
			fmt.Println("  No tool-calling capable models found.")
			fmt.Println("  (installed models lack tool/function-calling support)")
		}
		fmt.Println()
		fmt.Println("  Pull a tool-capable model manually, e.g.:")
		fmt.Println("    ollama pull qwen2.5-coder:14b")
		fmt.Println("    ollama pull llama3.1:8b")
		fmt.Println()
		fmt.Println("  Or try the llama.cpp provider for hardware-aware recommendations.")
		return nil, fmt.Errorf("no tool-capable models installed — pull one with: ollama pull <model>")
	}

	// Step 4: Show local tool-capable models, let user pick
	fmt.Println()
	fmt.Println("  Available models (tool-calling capable):")
	fmt.Println()
	for i, m := range toolCapable {
		sizeGB := float64(m.size) / (1024 * 1024 * 1024)
		marker := ""
		if i == 0 {
			marker = " *"
		}
		fmt.Printf("    [%d] %-45s %.1fGB%s\n", i+1, m.name, sizeGB, marker)
	}
	fmt.Println()

	fmt.Printf("  Enter model [1]: ")
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(ans)
	if ans == "" {
		ans = "1"
	}

	idx := 0
	if _, err := fmt.Sscanf(ans, "%d", &idx); err != nil || idx < 1 || idx > len(toolCapable) {
		// Treat as a model name
		return &OllamaSetupResult{Model: ans, BaseURL: baseURL}, nil
	}

	selected := toolCapable[idx-1]
	fmt.Printf("  ✓ Using %s\n", selected.name)
	return &OllamaSetupResult{Model: selected.name, BaseURL: baseURL}, nil
}

// RunLlamaCppSetup handles the interactive llama.cpp setup flow.
// It uses llmfit for hardware detection, model recommendation, and downloading.
// It reads from stdin and writes to stdout — must be called outside the TUI.
func RunLlamaCppSetup() (*LlamaCppSetupResult, error) {
	reader := bufio.NewReader(os.Stdin)

	// Step 1: Ensure llmfit is installed
	if err := EnsureLlmfit(); err != nil {
		return nil, fmt.Errorf("llmfit required for llama.cpp provider: %w", err)
	}

	// Step 2: Ensure llama.cpp is installed
	if err := EnsureLlamaCpp(); err != nil {
		return nil, fmt.Errorf("llama.cpp required: %w", err)
	}

	// Step 3: Detect hardware and recommend models
	fmt.Println()
	fmt.Println("  Analyzing your hardware...")
	hw, err := DetectHardware()
	if err != nil {
		return nil, fmt.Errorf("detecting hardware: %w", err)
	}

	fmt.Printf("  Detected: %s, %.0fGB RAM", hw.CPUName, hw.TotalRAMGB)
	if hw.UnifiedMemory {
		fmt.Print(" (unified)")
	}
	fmt.Println()

	recs := RecommendModels(hw)
	if len(recs) == 0 {
		fmt.Println("  No models fit your hardware.")
		fmt.Printf("  Enter a model name to download: ")
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(ans)
		if ans == "" {
			return nil, fmt.Errorf("no model selected")
		}
		return downloadModel(reader, ans)
	}

	fmt.Println()
	fmt.Println("  Recommended models for your hardware (all support tool-calling):")
	fmt.Println()
	for i, r := range recs {
		marker := ""
		if i == 0 {
			marker = " *"
		}
		quant := r.Quantization
		if quant == "" {
			quant = "-"
		}
		ctxStr := "-"
		if r.ContextLength > 0 {
			ctxStr = fmt.Sprintf("%dK ctx", r.ContextLength/1024)
		}
		color := fitColor(r.Fit)
		fmt.Printf("    %s[%d] %-45s %6s  %-7s  %8s  ~%.1fGB  %-16s  [%s]%s\033[0m\n",
			color, i+1, r.Name, r.Params, quant, ctxStr, r.RAMRequired, r.Category, r.Fit, marker)
	}
	fmt.Println()

	fmt.Printf("  Enter model [1]: ")
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)
	if choice == "" {
		choice = "1"
	}

	idx := 0
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(recs) {
		// Treat as a model name
		return downloadModel(reader, choice)
	}

	selected := recs[idx-1]
	return downloadModel(reader, selected.GGUFRepo)
}

// MLXSetupResult holds the result of the interactive MLX setup flow.
type MLXSetupResult struct {
	Model string // HuggingFace repo ID (e.g. "mlx-community/Qwen2.5-Coder-14B-Instruct-4bit")
}

// RunMLXSetup handles the interactive MLX provider setup flow.
// It uses llmfit for hardware detection and model recommendation, then returns
// the selected HuggingFace model ID. mlx_lm.server auto-downloads on first use.
// It reads from stdin and writes to stdout — must be called outside the TUI.
func RunMLXSetup() (*MLXSetupResult, error) {
	reader := bufio.NewReader(os.Stdin)

	// Step 1: Ensure mlx-lm is installed
	if err := EnsureMlxLm(); err != nil {
		return nil, fmt.Errorf("mlx-lm required for MLX provider: %w", err)
	}

	// Step 2: Ensure llmfit is installed (for recommendations)
	if err := EnsureLlmfit(); err != nil {
		return nil, fmt.Errorf("llmfit required for model recommendations: %w", err)
	}

	// Step 3: Detect hardware and recommend models
	fmt.Println()
	fmt.Println("  Analyzing your hardware...")
	hw, err := DetectHardware()
	if err != nil {
		return nil, fmt.Errorf("detecting hardware: %w", err)
	}

	fmt.Printf("  Detected: %s, %.0fGB RAM", hw.CPUName, hw.TotalRAMGB)
	if hw.UnifiedMemory {
		fmt.Print(" (unified)")
	}
	fmt.Println()

	recs := RecommendMLXModels(hw)
	if len(recs) == 0 {
		fmt.Println("  No MLX models recommended for your hardware.")
		fmt.Println("  Enter a HuggingFace model ID (e.g. mlx-community/Qwen2.5-Coder-14B-Instruct-4bit):")
		fmt.Printf("  > ")
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(ans)
		if ans == "" {
			return nil, fmt.Errorf("no model selected")
		}
		return &MLXSetupResult{Model: ans}, nil
	}

	fmt.Println()
	fmt.Println("  Recommended models for your hardware (all support tool-calling):")
	fmt.Println()
	for i, r := range recs {
		marker := ""
		if i == 0 {
			marker = " *"
		}
		quant := r.Quantization
		if quant == "" {
			quant = "-"
		}
		ctxStr := "-"
		if r.ContextLength > 0 {
			ctxStr = fmt.Sprintf("%dK ctx", r.ContextLength/1024)
		}
		color := fitColor(r.Fit)
		fmt.Printf("    %s[%d] %-45s %6s  %-7s  %8s  ~%.1fGB  %-16s  [%s]%s\033[0m\n",
			color, i+1, r.Name, r.Params, quant, ctxStr, r.RAMRequired, r.Category, r.Fit, marker)
	}
	fmt.Println()

	fmt.Printf("  Enter model [1]: ")
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)
	if choice == "" {
		choice = "1"
	}

	idx := 0
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(recs) {
		// Treat as a HuggingFace model ID
		return &MLXSetupResult{Model: choice}, nil
	}

	selected := recs[idx-1]
	fmt.Printf("  ✓ Selected %s (will download on first use)\n", selected.Name)
	return &MLXSetupResult{Model: selected.Name}, nil
}

// fitColor returns an ANSI escape code to color a row based on fit level.
func fitColor(fit string) string {
	switch strings.ToLower(fit) {
	case "perfect":
		return "\033[32m" // green
	case "good":
		return "\033[33m" // yellow
	case "tight":
		return "\033[31m" // red/orange
	default:
		return ""
	}
}

// downloadModel downloads a model via llmfit, retrying on failure.
// Uses a loop instead of recursion to avoid unbounded stack growth.
func downloadModel(reader *bufio.Reader, model string) (*LlamaCppSetupResult, error) {
	for {
		fmt.Println()
		fmt.Printf("  Downloading %s...\n", model)
		if err := DownloadModel(model); err != nil {
			fmt.Printf("\n  Download failed: %v\n", err)
			fmt.Println("  Enter a different model name, or press Enter to abort:")
			fmt.Printf("  > ")
			alt, _ := reader.ReadString('\n')
			alt = strings.TrimSpace(alt)
			if alt == "" {
				return nil, fmt.Errorf("download failed: %w", err)
			}
			model = alt
			continue
		}

		fmt.Printf("  ✓ Downloaded %s\n", model)
		return &LlamaCppSetupResult{Model: model}, nil
	}
}
