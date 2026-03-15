// Package shell handles shell state, built-ins, and input dispatch.
package shell

import (
	"os"
	"strings"
	"sync"
)

// EnvState holds the mutable shell environment (cwd, env vars, aliases).
// All access is synchronized for concurrent use between TUI and agent goroutines.
type EnvState struct {
	mu      sync.RWMutex
	cwd     string
	env     map[string]string
	aliases map[string]string
}

// NewEnvState creates an EnvState from the current process environment.
func NewEnvState() *EnvState {
	cwd, _ := os.Getwd()
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}
	return &EnvState{
		cwd:     cwd,
		env:     env,
		aliases: make(map[string]string),
	}
}

// GetCwd returns the current working directory.
func (e *EnvState) GetCwd() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cwd
}

// SetCwd sets the current working directory.
func (e *EnvState) SetCwd(cwd string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cwd = cwd
}

// ChangeCwd atomically updates cwd, PWD, and OLDPWD under a single lock.
func (e *EnvState) ChangeCwd(newCwd string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.env["OLDPWD"] = e.cwd
	e.cwd = newCwd
	e.env["PWD"] = newCwd
}

// ToSlice converts the env map to os.Environ format (KEY=VALUE).
func (e *EnvState) ToSlice() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]string, 0, len(e.env))
	for k, v := range e.env {
		result = append(result, k+"="+v)
	}
	return result
}

// Get returns an env var value.
func (e *EnvState) Get(key string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.env[key]
}

// Set sets an env var.
func (e *EnvState) Set(key, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.env[key] = value
}

// Unset removes an env var.
func (e *EnvState) Unset(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.env, key)
}

// GetAlias returns an alias value and whether it exists.
func (e *EnvState) GetAlias(name string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	v, ok := e.aliases[name]
	return v, ok
}

// SetAlias sets an alias.
func (e *EnvState) SetAlias(name, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.aliases[name] = value
}

// DeleteAlias removes an alias.
func (e *EnvState) DeleteAlias(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.aliases, name)
}

// AllAliases returns a copy of all aliases.
func (e *EnvState) AllAliases() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make(map[string]string, len(e.aliases))
	for k, v := range e.aliases {
		result[k] = v
	}
	return result
}

// AllEnv returns a copy of all env vars.
func (e *EnvState) AllEnv() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make(map[string]string, len(e.env))
	for k, v := range e.env {
		result[k] = v
	}
	return result
}
