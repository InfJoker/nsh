// Package config handles loading and creating ~/.nsh/config.toml.
package config

import (
	"bytes"
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

// ProviderConfig describes the infrastructure for a provider.
type ProviderConfig struct {
	Type    string `toml:"type"`               // "anthropic", "ollama", "llama.cpp", "mlx", "mock"
	BaseURL string `toml:"base_url,omitempty"` // custom endpoint (omit for native/default)
	APIKey  string `toml:"api_key,omitempty"`  // for anthropic (env var fallback)
}

// Preset describes a model selection on a configured provider.
type Preset struct {
	Provider string `toml:"provider"` // references a key in [providers]
	Model    string `toml:"model"`
}

// Config is the top-level nsh configuration.
type Config struct {
	ActivePreset string                    `toml:"preset"`
	Providers    map[string]ProviderConfig `toml:"providers"`
	Presets      map[string]Preset         `toml:"presets"`

	Theme       string      `toml:"theme"`
	Shell       string      `toml:"shell"`
	MaxSteps    int         `toml:"max_steps"`
	Permissions Permissions `toml:"permissions"`

	// Runtime-only fields resolved from the active preset at startup.
	// Not persisted to TOML.
	Provider string `toml:"-"`
	Model    string `toml:"-"`
	BaseURL  string `toml:"-"`

	mu           sync.Mutex `toml:"-"`
	learnedRules []Rule     `toml:"-"`
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

// GetActivePreset resolves the active preset and its provider config.
// Returns zero values if not configured.
func (c *Config) GetActivePreset() (name string, preset Preset, provider ProviderConfig) {
	name = c.ActivePreset
	if name == "" || c.Presets == nil {
		return
	}
	preset, ok := c.Presets[name]
	if !ok {
		return
	}
	if c.Providers != nil {
		provider = c.Providers[preset.Provider]
	}
	return
}

// ResolvePreset looks up a preset by name and its provider config.
func (c *Config) ResolvePreset(name string) (Preset, ProviderConfig, error) {
	if c.Presets == nil {
		return Preset{}, ProviderConfig{}, fmt.Errorf("no presets configured")
	}
	preset, ok := c.Presets[name]
	if !ok {
		return Preset{}, ProviderConfig{}, fmt.Errorf("preset %q not found", name)
	}
	if c.Providers == nil {
		return preset, ProviderConfig{}, fmt.Errorf("provider %q not configured", preset.Provider)
	}
	prov, ok := c.Providers[preset.Provider]
	if !ok {
		return preset, ProviderConfig{}, fmt.Errorf("provider %q referenced by preset %q not found", preset.Provider, name)
	}
	return preset, prov, nil
}

// ListPresets returns all configured presets.
func (c *Config) ListPresets() map[string]Preset {
	if c.Presets == nil {
		return map[string]Preset{}
	}
	return c.Presets
}

// SavePreset adds or updates a preset and writes the config.
func (c *Config) SavePreset(name string, p Preset) error {
	if c.Presets == nil {
		c.Presets = make(map[string]Preset)
	}
	c.Presets[name] = p
	return c.Save()
}

// SaveProvider adds or updates a provider and writes the config.
func (c *Config) SaveProvider(name string, p ProviderConfig) error {
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}
	c.Providers[name] = p
	return c.Save()
}

// SetActivePreset changes the active preset and writes the config.
func (c *Config) SetActivePreset(name string) error {
	c.ActivePreset = name
	return c.Save()
}

// Save writes the entire config to ~/.nsh/config.toml using proper TOML encoding.
func (c *Config) Save() error {
	configPath := filepath.Join(NshDir(), "config.toml")
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(c); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	return os.WriteFile(configPath, buf.Bytes(), 0644)
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

// ResolveActivePreset populates the runtime Provider/Model fields from the active preset.
// Call after Load() and any --preset flag override.
func (c *Config) ResolveActivePreset() error {
	if c.ActivePreset == "" {
		return nil // no preset configured — caller handles setup
	}
	preset, prov, err := c.ResolvePreset(c.ActivePreset)
	if err != nil {
		return err
	}
	c.Provider = prov.Type
	c.Model = preset.Model
	// BaseURL: use provider's custom URL if set, otherwise blank (native/default)
	c.BaseURL = prov.BaseURL
	// API key: use provider config, fall back to env
	if prov.Type == "anthropic" && prov.APIKey != "" {
		os.Setenv("ANTHROPIC_API_KEY", prov.APIKey)
	}
	return nil
}

// SaveProviderFull saves the current runtime provider/model as the active preset.
// This bridges the old API used by applyProviderSwitch in the TUI.
func (c *Config) SaveProviderFull(providerType, model, baseURL string) error {
	presetName := c.ActivePreset
	if presetName == "" {
		presetName = "default"
	}
	// Find or create a provider entry for this type
	provName := ""
	if c.Providers != nil {
		for name, p := range c.Providers {
			if p.Type == providerType {
				provName = name
				break
			}
		}
	}
	if provName == "" {
		provName = providerType
		if c.Providers == nil {
			c.Providers = make(map[string]ProviderConfig)
		}
		c.Providers[provName] = ProviderConfig{Type: providerType, BaseURL: baseURL}
	}
	if c.Presets == nil {
		c.Presets = make(map[string]Preset)
	}
	c.Presets[presetName] = Preset{Provider: provName, Model: model}
	c.ActivePreset = presetName
	c.Provider = providerType
	c.Model = model
	c.BaseURL = baseURL
	return c.Save()
}

// HasPresets returns true if the config has the new presets format.
func (c *Config) HasPresets() bool {
	return len(c.Presets) > 0
}

func defaultConfig() string {
	return strings.TrimLeft(`
theme = "catppuccin"
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
`, "\n")
}
