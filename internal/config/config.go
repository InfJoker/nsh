// Package config handles loading and creating ~/.nsh/config.toml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// PermissionAction is the action for a permission rule.
type PermissionAction string

const (
	ActionAllow PermissionAction = "allow"
	ActionAsk   PermissionAction = "ask"
	ActionDeny  PermissionAction = "deny"
)

// Rule is a single permission rule.
type Rule struct {
	Pattern string           `toml:"pattern"`
	Action  PermissionAction `toml:"action"`
}

// Permissions holds the permission configuration.
type Permissions struct {
	Dangerous []string `toml:"dangerous"`
	Rules     []Rule   `toml:"rules"`
}

// Config is the top-level nsh configuration.
type Config struct {
	Provider    string      `toml:"provider"`
	Model       string      `toml:"model"`
	BaseURL     string      `toml:"base_url"`
	Theme       string      `toml:"theme"`
	Shell       string      `toml:"shell"`
	MaxSteps    int         `toml:"max_steps"`
	Permissions Permissions `toml:"permissions"`

	mu           sync.Mutex
	learnedRules []Rule
}

// NshDir returns the base config directory (~/.nsh).
func NshDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nsh")
}

// DataDir returns the data directory (~/.nsh/data).
func DataDir() string {
	return filepath.Join(NshDir(), "data")
}

// AllRules returns config rules merged with learned rules. Config rules take priority.
func (c *Config) AllRules() []Rule {
	c.mu.Lock()
	defer c.mu.Unlock()
	rules := make([]Rule, 0, len(c.Permissions.Rules)+len(c.learnedRules))
	rules = append(rules, c.Permissions.Rules...)
	rules = append(rules, c.learnedRules...)
	return rules
}

// IsDangerous checks if a command base matches the dangerous list.
func (c *Config) IsDangerous(cmd string) bool {
	for _, d := range c.Permissions.Dangerous {
		if cmd == d {
			return true
		}
		// Check if cmd starts with dangerous prefix
		if len(cmd) > len(d) && cmd[:len(d)] == d && (cmd[len(d)] == ' ') {
			return true
		}
	}
	return false
}

// AppendLearnedRule adds a rule to learned_rules.toml.
func (c *Config) AppendLearnedRule(r Rule) error {
	c.mu.Lock()
	c.learnedRules = append(c.learnedRules, r)
	c.mu.Unlock()

	path := filepath.Join(DataDir(), "learned_rules.toml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "\n[[rules]]\npattern = %q\naction = \"allow\"\n", r.Pattern)
	return err
}

// Load reads config from ~/.nsh/config.toml, creating defaults if needed.
func Load() (*Config, error) {
	dir := NshDir()
	dataDir := DataDir()

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	configPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte(defaultConfig()), 0644); err != nil {
			return nil, fmt.Errorf("writing default config: %w", err)
		}
	}

	var cfg Config
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Apply defaults
	if cfg.Shell == "" {
		cfg.Shell = os.Getenv("SHELL")
		if cfg.Shell == "" {
			cfg.Shell = "/bin/sh"
		}
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 25
	}
	if cfg.Theme == "" {
		cfg.Theme = "catppuccin"
	}
	// Provider and model defaults are handled by the startup flow in main.go
	// to allow auto-detection and interactive selection.

	// Load learned rules
	learnedPath := filepath.Join(dataDir, "learned_rules.toml")
	if data, err := os.ReadFile(learnedPath); err == nil && len(data) > 0 {
		var learned struct {
			Rules []Rule `toml:"rules"`
		}
		if _, err := toml.Decode(string(data), &learned); err == nil {
			cfg.learnedRules = learned.Rules
		}
	}

	return &cfg, nil
}

// SaveProvider writes the provider, model, and base_url to the config file.
func (c *Config) SaveProvider(provider, model string) error {
	return c.SaveProviderFull(provider, model, "")
}

// SaveProviderFull writes the provider, model, and base_url to the config file.
func (c *Config) SaveProviderFull(provider, model, baseURL string) error {
	c.Provider = provider
	c.Model = model
	c.BaseURL = baseURL

	configPath := filepath.Join(NshDir(), "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	content := string(data)
	// Replace the provider/model lines — match exact key names only
	lines := strings.Split(content, "\n")
	var result []string
	providerSet, modelSet, baseURLSet := false, false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip comments
		if strings.HasPrefix(trimmed, "#") {
			result = append(result, line)
			continue
		}
		// Match exact key names only
		key := extractTOMLKey(trimmed)
		switch key {
		case "provider":
			result = append(result, fmt.Sprintf("provider = %q", provider))
			providerSet = true
		case "model":
			result = append(result, fmt.Sprintf("model = %q", model))
			modelSet = true
		case "base_url":
			if baseURL != "" {
				result = append(result, fmt.Sprintf("base_url = %q", baseURL))
			}
			// else: drop the line (empty base_url)
			baseURLSet = true
		default:
			result = append(result, line)
		}
	}
	if !providerSet {
		idx := 0
		for idx < len(result) && strings.HasPrefix(strings.TrimSpace(result[idx]), "#") {
			idx++
		}
		line := fmt.Sprintf("provider = %q", provider)
		result = append(result[:idx], append([]string{line}, result[idx:]...)...)
	}
	if !modelSet {
		idx := 0
		for idx < len(result) && (strings.HasPrefix(strings.TrimSpace(result[idx]), "#") || extractTOMLKey(strings.TrimSpace(result[idx])) == "provider") {
			idx++
		}
		line := fmt.Sprintf("model = %q", model)
		result = append(result[:idx], append([]string{line}, result[idx:]...)...)
	}
	if !baseURLSet && baseURL != "" {
		// Insert after model line
		idx := 0
		for idx < len(result) {
			key := extractTOMLKey(strings.TrimSpace(result[idx]))
			if key == "model" {
				idx++
				break
			}
			idx++
		}
		line := fmt.Sprintf("base_url = %q", baseURL)
		result = append(result[:idx], append([]string{line}, result[idx:]...)...)
	}

	return os.WriteFile(configPath, []byte(strings.Join(result, "\n")), 0644)
}

// extractTOMLKey returns the exact key name from a TOML line, or "" if not a key=value line.
func extractTOMLKey(line string) string {
	if !strings.Contains(line, "=") {
		return ""
	}
	key, _, _ := strings.Cut(line, "=")
	return strings.TrimSpace(key)
}

func defaultConfig() string {
	return `# provider: anthropic, copilot, or ollama
# Set to "" to be prompted on first run.
provider = ""
model = ""
theme = "catppuccin"
# shell defaults to $SHELL
max_steps = 25

[permissions]
dangerous = ["rm", "sudo", "chmod", "chown", "mkfs", "dd", "git reset --hard", "git push --force", "npm publish"]

[[permissions.rules]]
pattern = "ls *"
action = "allow"

[[permissions.rules]]
pattern = "cat *"
action = "allow"

[[permissions.rules]]
pattern = "head *"
action = "allow"

[[permissions.rules]]
pattern = "tail *"
action = "allow"

[[permissions.rules]]
pattern = "wc *"
action = "allow"

[[permissions.rules]]
pattern = "pwd"
action = "allow"

[[permissions.rules]]
pattern = "echo *"
action = "allow"

[[permissions.rules]]
pattern = "find *"
action = "allow"

[[permissions.rules]]
pattern = "grep *"
action = "allow"

[[permissions.rules]]
pattern = "rg *"
action = "allow"

[[permissions.rules]]
pattern = "which *"
action = "allow"

[[permissions.rules]]
pattern = "cd *"
action = "allow"

[[permissions.rules]]
pattern = "mkdir *"
action = "allow"

[[permissions.rules]]
pattern = "git status"
action = "allow"

[[permissions.rules]]
pattern = "git log *"
action = "allow"

[[permissions.rules]]
pattern = "git diff *"
action = "allow"

[[permissions.rules]]
pattern = "git branch *"
action = "allow"

[[permissions.rules]]
pattern = "git show *"
action = "allow"

[[permissions.rules]]
pattern = "go test *"
action = "allow"

[[permissions.rules]]
pattern = "git push --force *"
action = "deny"

[[permissions.rules]]
pattern = "git reset --hard *"
action = "deny"

[[permissions.rules]]
pattern = "sudo *"
action = "deny"

[[permissions.rules]]
pattern = "chmod 777 *"
action = "deny"

[[permissions.rules]]
pattern = "mkfs *"
action = "deny"

[[permissions.rules]]
pattern = "dd *"
action = "deny"
`
}
