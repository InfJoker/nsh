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
	Model string // HF repo with optional quant (e.g. "Qwen/Qwen2.5-Coder-14B-Instruct-GGUF:Q4_K_M")
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
// Prompts the user for a HuggingFace GGUF repo. llama-server auto-downloads via --hf-repo.
// It reads from stdin and writes to stdout — must be called outside the TUI.
func RunLlamaCppSetup() (*LlamaCppSetupResult, error) {
	reader := bufio.NewReader(os.Stdin)

	// Step 1: Ensure llama.cpp is installed
	if err := EnsureLlamaCpp(); err != nil {
		return nil, fmt.Errorf("llama.cpp required: %w", err)
	}

	// Step 2: Show suggestions and prompt for model
	fmt.Println()
	fmt.Println("  Enter a HuggingFace GGUF model repo.")
	fmt.Println("  Format: owner/repo:quant (quant defaults to Q4_K_M)")
	fmt.Println()
	fmt.Println("  Popular models for tool-calling:")
	fmt.Println("    Qwen/Qwen2.5-Coder-14B-Instruct-GGUF       (14B, coding)")
	fmt.Println("    Qwen/Qwen2.5-Coder-7B-Instruct-GGUF        (7B, coding)")
	fmt.Println("    Qwen/Qwen2.5-14B-Instruct-GGUF             (14B, general)")
	fmt.Println("    mistralai/Mistral-Small-24B-Instruct-2501   (24B, general)")
	fmt.Println()
	fmt.Println("  Find more: https://huggingface.co/models?sort=trending&search=GGUF")
	fmt.Println("  Tools: llmfit recommend, llmchecker — for hardware-aware recommendations")
	fmt.Println()
	fmt.Printf("  Model: ")
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return nil, fmt.Errorf("no model selected")
	}
	fmt.Printf("  ✓ Selected %s (will download on first run)\n", ans)
	return &LlamaCppSetupResult{Model: ans}, nil
}

// HypuraSetupResult holds the result of the interactive Hypura setup flow.
type HypuraSetupResult struct {
	Model string // local GGUF file path
}

// RunHypuraSetup handles the interactive Hypura provider setup flow.
// Prompts the user for a local GGUF model path.
// It reads from stdin and writes to stdout — must be called outside the TUI.
func RunHypuraSetup() (*HypuraSetupResult, error) {
	reader := bufio.NewReader(os.Stdin)

	if !HypuraInstalled() {
		fmt.Println()
		fmt.Println("  Hypura not found on PATH.")
		fmt.Println("  Install from source:")
		fmt.Println("    git clone --recurse-submodules https://github.com/t8/hypura.git")
		fmt.Println("    cd hypura && cargo build --release")
		fmt.Println("    # Add target/release/hypura to your PATH")
		fmt.Println()
		return nil, fmt.Errorf("hypura not installed — see https://github.com/t8/hypura")
	}

	fmt.Println()
	fmt.Println("  Enter the path to a GGUF model file.")
	fmt.Println("  Hypura places tensors across GPU/RAM/NVMe — models larger than RAM are OK.")
	fmt.Println()
	fmt.Println("  Popular GGUF models (download from HuggingFace):")
	fmt.Println("    Qwen/Qwen2.5-Coder-14B-Instruct-GGUF       (14B, coding)")
	fmt.Println("    Qwen/Qwen2.5-Coder-32B-Instruct-GGUF       (32B, coding)")
	fmt.Println("    bartowski/Mixtral-8x7B-Instruct-v0.1-GGUF   (47B MoE, general)")
	fmt.Println("    bartowski/Meta-Llama-3.1-70B-Instruct-GGUF  (70B, general)")
	fmt.Println()
	fmt.Println("  Find more: https://huggingface.co/models?sort=trending&search=GGUF")
	fmt.Println()
	fmt.Printf("  Model path: ")
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return nil, fmt.Errorf("no model path provided")
	}

	// Expand ~ if present
	if strings.HasPrefix(ans, "~/") {
		home, _ := os.UserHomeDir()
		ans = home + ans[1:]
	}

	// Validate file exists
	if _, err := os.Stat(ans); err != nil {
		return nil, fmt.Errorf("model file not found: %s", ans)
	}

	fmt.Printf("  ✓ Using %s\n", ans)
	return &HypuraSetupResult{Model: ans}, nil
}

// MLXSetupResult holds the result of the interactive MLX setup flow.
type MLXSetupResult struct {
	Model string // HuggingFace repo ID (e.g. "mlx-community/Qwen2.5-Coder-14B-Instruct-4bit")
}

// RunMLXSetup handles the interactive MLX provider setup flow.
// Prompts the user for a HuggingFace MLX model. mlx_lm.server auto-downloads on first use.
// It reads from stdin and writes to stdout — must be called outside the TUI.
func RunMLXSetup() (*MLXSetupResult, error) {
	reader := bufio.NewReader(os.Stdin)

	// Step 1: Ensure mlx-lm is installed
	if err := EnsureMlxLm(); err != nil {
		return nil, fmt.Errorf("mlx-lm required for MLX provider: %w", err)
	}

	// Step 2: Show suggestions and prompt for model
	fmt.Println()
	fmt.Println("  Enter a HuggingFace MLX model repo.")
	fmt.Println("  Models from mlx-community are pre-quantized for Apple Silicon.")
	fmt.Println()
	fmt.Println("  Popular models for tool-calling:")
	fmt.Println("    mlx-community/Qwen2.5-Coder-14B-Instruct-4bit   (14B, coding)")
	fmt.Println("    mlx-community/Qwen2.5-14B-Instruct-4bit         (14B, general)")
	fmt.Println("    mlx-community/Qwen2.5-Coder-7B-Instruct-4bit    (7B, coding)")
	fmt.Println("    mlx-community/Mistral-Small-24B-Instruct-2501-4bit (24B, general)")
	fmt.Println()
	fmt.Println("  Find more: https://huggingface.co/mlx-community")
	fmt.Println("  Tools: llmfit recommend, llmchecker — for hardware-aware recommendations")
	fmt.Println()
	fmt.Println("  Note: models under 14B often fail to use tools correctly.")
	fmt.Printf("  Model: ")
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return nil, fmt.Errorf("no model selected")
	}
	fmt.Printf("  ✓ Selected %s (will download on first use)\n", ans)
	return &MLXSetupResult{Model: ans}, nil
}
