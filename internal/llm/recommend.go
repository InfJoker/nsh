package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

// SystemInfo holds hardware info detected by llmfit.
type SystemInfo struct {
	TotalRAMGB    float64 `json:"total_ram_gb"`
	GPUVRAMGB     float64 `json:"gpu_vram_gb"`
	CPUName       string  `json:"cpu_name"`
	UnifiedMemory bool    `json:"unified_memory"`
}

// ModelOption is a model recommended by llmfit for local use.
type ModelOption struct {
	Name          string  // HuggingFace display name
	GGUFRepo      string  // GGUF source repo for download (e.g. "Qwen/Qwen3.5-9B-GGUF")
	Params        string  // parameter count display
	RAMRequired   float64 // approximate GB needed
	Category      string  // use case from llmfit
	Fit           string  // "perfect", "good", "tight"
	ContextLength int     // context window size in tokens
	Quantization  string  // quantization level (e.g. "Q4_K_M")
	Runtime       string  // "gguf" or "mlx"
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

// DetectHardware uses llmfit to detect system hardware.
func DetectHardware() (*SystemInfo, error) {
	out, err := exec.Command("llmfit", "recommend", "--json", "--limit", "1").Output()
	if err != nil {
		return nil, fmt.Errorf("running llmfit: %w", err)
	}

	var result struct {
		System SystemInfo `json:"system"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parsing llmfit output: %w", err)
	}

	return &result.System, nil
}

// llmfitModel is a model from llmfit's database.
type llmfitModel struct {
	Name           string   `json:"name"`
	ParameterCount string   `json:"parameter_count"`
	MinRAMGB       float64  `json:"min_ram_gb"`
	UseCase        string   `json:"use_case"`
	ContextLength  int      `json:"context_length"`
	Quantization   string   `json:"quantization"`
	Capabilities   []string `json:"capabilities"`
	GGUFSources    []struct {
		Repo string `json:"repo"`
	} `json:"gguf_sources"`
}

// RecommendModels queries llmfit's model database for tool-calling models
// that fit the detected hardware. Used by the llama.cpp provider path.
func RecommendModels(hw *SystemInfo) []ModelOption {
	out, err := exec.Command("llmfit", "list", "--json").Output()
	if err != nil {
		return nil
	}

	var models []llmfitModel
	if err := json.Unmarshal(out, &models); err != nil {
		return nil
	}

	// Available memory with headroom for OS
	usableGB := hw.TotalRAMGB - 4.0
	if hw.GPUVRAMGB > 0 && hw.GPUVRAMGB < hw.TotalRAMGB {
		usableGB = hw.GPUVRAMGB - 4.0
	}
	if usableGB < 2.0 {
		usableGB = 2.0
	}

	// Filter: tool_use capable + has GGUF source + fits hardware
	// Prefer -Instruct variants; deduplicate by base model family+size
	type candidate struct {
		model    llmfitModel
		ggufRepo string
		fit      string
	}

	var candidates []candidate
	for _, m := range models {
		if !hasCapability(m.Capabilities, "tool_use") {
			continue
		}
		if len(m.GGUFSources) == 0 {
			continue
		}
		if m.MinRAMGB > usableGB {
			continue
		}

		fit := fitLevel(m.MinRAMGB, usableGB)
		candidates = append(candidates, candidate{
			model:    m,
			ggufRepo: m.GGUFSources[0].Repo,
			fit:      fit,
		})
	}

	// Deduplicate: prefer Instruct variants, group by param count + base family
	type dedupeKey struct {
		family string
		params string
	}
	best := make(map[dedupeKey]candidate)
	for _, c := range candidates {
		key := dedupeKey{
			family: extractFamily(c.model.Name),
			params: c.model.ParameterCount,
		}
		existing, exists := best[key]
		if !exists || preferOver(c.model.Name, existing.model.Name) {
			best[key] = c
		}
	}

	// Collect and sort by RAM descending (biggest that fits first)
	var results []ModelOption
	for _, c := range best {
		results = append(results, ModelOption{
			Name:          c.model.Name,
			GGUFRepo:      c.ggufRepo,
			Params:        c.model.ParameterCount,
			RAMRequired:   c.model.MinRAMGB,
			Category:      c.model.UseCase,
			Fit:           c.fit,
			ContextLength: c.model.ContextLength,
			Quantization:  c.model.Quantization,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].RAMRequired > results[j].RAMRequired
	})

	// Cap at 15 to keep the list manageable
	if len(results) > 15 {
		results = results[:15]
	}

	return results
}

// llmfitRecommendModel is a model from llmfit's recommend output.
type llmfitRecommendModel struct {
	Name            string  `json:"name"`
	ParameterCount  string  `json:"parameter_count"`
	MemoryRequired  float64 `json:"memory_required_gb"`
	Category        string  `json:"category"`
	FitLevel        string  `json:"fit_level"`
	ContextLength   int     `json:"context_length"`
	BestQuant       string  `json:"best_quant"`
	Runtime         string  `json:"runtime"`
}

// RecommendMLXModels queries llmfit's recommendation engine for MLX-runtime models
// that fit the detected hardware. Used by the MLX provider path on Apple Silicon.
func RecommendMLXModels(hw *SystemInfo) []ModelOption {
	out, err := exec.Command("llmfit", "recommend", "--json").Output()
	if err != nil {
		return nil
	}

	var result struct {
		Models []llmfitRecommendModel `json:"models"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil
	}

	var options []ModelOption
	for _, m := range result.Models {
		if strings.ToUpper(m.Runtime) != "MLX" {
			continue
		}
		// Skip models with NVIDIA-specific quantization (NVFP4, FP8) —
		// these require TensorRT-LLM/vLLM, not mlx-lm.
		nameLower := strings.ToLower(m.Name)
		if strings.Contains(nameLower, "nvfp") || strings.Contains(nameLower, "-fp8") {
			continue
		}
		options = append(options, ModelOption{
			Name:          m.Name,
			Params:        m.ParameterCount,
			RAMRequired:   m.MemoryRequired,
			Category:      m.Category,
			Fit:           m.FitLevel,
			ContextLength: m.ContextLength,
			Quantization:  m.BestQuant,
			Runtime:       "mlx",
		})
	}

	// Cap at 15
	if len(options) > 15 {
		options = options[:15]
	}

	return options
}

func hasCapability(caps []string, target string) bool {
	for _, c := range caps {
		if c == target {
			return true
		}
	}
	return false
}

func fitLevel(required, available float64) string {
	ratio := required / available
	switch {
	case ratio <= 0.6:
		return "perfect"
	case ratio <= 0.85:
		return "good"
	case ratio <= 1.0:
		return "tight"
	default:
		return "too large"
	}
}

// extractFamily returns a normalized base model family from a HuggingFace name.
// e.g. "Qwen/Qwen2.5-Coder-14B-Instruct" -> "qwen2.5-coder"
func extractFamily(name string) string {
	// Strip org prefix
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.ToLower(name)
	// Remove common suffixes
	for _, suffix := range []string{"-instruct", "-base", "-chat", "-fp8", "-1m"} {
		name = strings.TrimSuffix(name, suffix)
	}
	// Remove parameter size suffix (e.g. "-14b", "-7b", "-0.5b")
	parts := strings.Split(name, "-")
	var cleaned []string
	for _, p := range parts {
		if isParamSize(p) {
			continue
		}
		cleaned = append(cleaned, p)
	}
	return strings.Join(cleaned, "-")
}

func isParamSize(s string) bool {
	s = strings.TrimSuffix(s, "b")
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// preferOver returns true if name a should be preferred over name b.
// Prefers: Instruct > Chat > Base; original org > community repacks.
func preferOver(a, b string) bool {
	scoreA := instructScore(a)
	scoreB := instructScore(b)
	if scoreA != scoreB {
		return scoreA > scoreB
	}
	// Prefer models from the original org (shorter name usually)
	return len(a) < len(b)
}

func instructScore(name string) int {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "instruct") {
		return 3
	}
	if strings.Contains(lower, "chat") {
		return 2
	}
	return 1
}
