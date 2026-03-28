package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuiltinResult holds the result of a built-in command execution.
type BuiltinResult struct {
	Output           string
	NewCwd           string // non-empty if cwd changed
	IsBuiltin        bool
	IsProviderSwitch bool   // true if TUI should show provider selection overlay
	IsPresetSwitch   bool   // true if TUI should show preset selection overlay
	PresetArg        string // preset name for direct switch (e.g. !presets light)
}

// IsBuiltin checks if a command is a built-in without executing it.
func IsBuiltin(command string) bool {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "cd", "export", "unset", "alias", "unalias", "provider", "presets", "providers":
		return true
	}
	return false
}

// ExecBuiltin checks if a command is a built-in and executes it.
// Returns IsBuiltin=false if the command is not a built-in.
func ExecBuiltin(env *EnvState, command string) BuiltinResult {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return BuiltinResult{}
	}

	// Check alias expansion
	if alias, ok := env.GetAlias(parts[0]); ok {
		expanded := alias + " " + strings.Join(parts[1:], " ")
		parts = strings.Fields(expanded)
	}

	switch parts[0] {
	case "cd":
		return execCd(env, parts[1:])
	case "export":
		return execExport(env, parts[1:])
	case "unset":
		return execUnset(env, parts[1:])
	case "alias":
		return execAlias(env, parts[1:])
	case "unalias":
		return execUnalias(env, parts[1:])
	case "provider", "providers":
		return BuiltinResult{IsBuiltin: true, IsProviderSwitch: true}
	case "presets":
		arg := ""
		if len(parts) > 1 {
			arg = parts[1]
		}
		return BuiltinResult{IsBuiltin: true, IsPresetSwitch: true, PresetArg: arg}
	default:
		return BuiltinResult{}
	}
}

func execCd(env *EnvState, args []string) BuiltinResult {
	var target string
	switch len(args) {
	case 0:
		home, err := os.UserHomeDir()
		if err != nil {
			return BuiltinResult{IsBuiltin: true, Output: fmt.Sprintf("cd: %v", err)}
		}
		target = home
	case 1:
		target = args[0]
		if target == "-" {
			old := env.Get("OLDPWD")
			if old == "" {
				return BuiltinResult{IsBuiltin: true, Output: "cd: OLDPWD not set"}
			}
			target = old
		}
		if target == "~" || strings.HasPrefix(target, "~/") {
			home, _ := os.UserHomeDir()
			target = filepath.Join(home, target[1:])
		}
	default:
		return BuiltinResult{IsBuiltin: true, Output: "cd: too many arguments"}
	}

	// Resolve relative to current cwd
	if !filepath.IsAbs(target) {
		target = filepath.Join(env.GetCwd(), target)
	}
	target = filepath.Clean(target)

	// Verify directory exists
	info, err := os.Stat(target)
	if err != nil {
		return BuiltinResult{IsBuiltin: true, Output: fmt.Sprintf("cd: %v", err)}
	}
	if !info.IsDir() {
		return BuiltinResult{IsBuiltin: true, Output: fmt.Sprintf("cd: %s: Not a directory", target)}
	}

	env.ChangeCwd(target)

	return BuiltinResult{IsBuiltin: true, NewCwd: target}
}

func execExport(env *EnvState, args []string) BuiltinResult {
	if len(args) == 0 {
		// List all exports
		var sb strings.Builder
		for k, v := range env.AllEnv() {
			fmt.Fprintf(&sb, "export %s=%q\n", k, v)
		}
		return BuiltinResult{IsBuiltin: true, Output: sb.String()}
	}

	for _, arg := range args {
		if k, v, ok := strings.Cut(arg, "="); ok {
			env.Set(k, v)
		}
	}
	return BuiltinResult{IsBuiltin: true}
}

func execUnset(env *EnvState, args []string) BuiltinResult {
	for _, arg := range args {
		env.Unset(arg)
	}
	return BuiltinResult{IsBuiltin: true}
}

func execAlias(env *EnvState, args []string) BuiltinResult {
	if len(args) == 0 {
		var sb strings.Builder
		for k, v := range env.AllAliases() {
			fmt.Fprintf(&sb, "alias %s=%q\n", k, v)
		}
		return BuiltinResult{IsBuiltin: true, Output: sb.String()}
	}

	for _, arg := range args {
		if k, v, ok := strings.Cut(arg, "="); ok {
			env.SetAlias(k, v)
		} else {
			if v, ok := env.GetAlias(arg); ok {
				return BuiltinResult{IsBuiltin: true, Output: fmt.Sprintf("alias %s=%q", arg, v)}
			}
			return BuiltinResult{IsBuiltin: true, Output: fmt.Sprintf("alias: %s: not found", arg)}
		}
	}
	return BuiltinResult{IsBuiltin: true}
}

func execUnalias(env *EnvState, args []string) BuiltinResult {
	for _, arg := range args {
		env.DeleteAlias(arg)
	}
	return BuiltinResult{IsBuiltin: true}
}
