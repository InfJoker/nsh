package shell

import (
	"os"
	"testing"
)

func TestIsBuiltin(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		// Simple builtins
		{"cd", true},
		{"cd /tmp", true},
		{"cd ~", true},
		{"export FOO=bar", true},
		{"unset FOO", true},
		{"alias ll=ls", true},
		{"presets", true},
		{"presets light", true},

		// Compound commands — must NOT be treated as builtins
		{"cd /tmp && ls", false},
		{"cd /tmp || echo fail", false},
		{"cd /tmp; ls", false},
		{"export FOO=bar && echo done", false},
		{"cd /tmp | cat", false},

		// Shell parses FOO=a&&b as "FOO=a && b" — compound command
		{"export FOO=a&&b", false},
		// Quoted operators are safe — stays a simple builtin
		{"export FOO='a&&b'", true},

		// Non-builtins
		{"ls -la", false},
		{"echo hello", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := IsBuiltin(tt.command)
			if got != tt.want {
				t.Errorf("IsBuiltin(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestIsCompoundCommand(t *testing.T) {
	tests := []struct {
		command  string
		compound bool
	}{
		{"ls", false},
		{"cd /tmp", false},
		{"cd /tmp && ls", true},
		{"cd /tmp || echo fail", true},
		{"cd /tmp; ls -la", true},
		{"ls | grep foo", true},
		{"echo hello && echo world", true},
		{"export FOO=a&&b", true},  // shell parses as "FOO=a && b" — compound
		{"export FOO='a&&b'", false}, // quoted — stays simple
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := isCompoundCommand(tt.command)
			if got != tt.compound {
				t.Errorf("isCompoundCommand(%q) = %v, want %v", tt.command, got, tt.compound)
			}
		})
	}
}

func TestExecCd_ExitCodes(t *testing.T) {
	env := NewEnvState()

	// Success case
	result := ExecBuiltin(env, "cd /tmp")
	if result.ExitCode != 0 {
		t.Errorf("cd /tmp: got exit code %d, want 0", result.ExitCode)
	}

	// Nonexistent directory
	result = ExecBuiltin(env, "cd /nonexistent_dir_12345")
	if result.ExitCode != 1 {
		t.Errorf("cd /nonexistent: got exit code %d, want 1", result.ExitCode)
	}
	if result.Output == "" {
		t.Error("cd /nonexistent: expected error output")
	}

	// Too many arguments (simple command, not compound)
	result = ExecBuiltin(env, "cd a b")
	if result.ExitCode != 1 {
		t.Errorf("cd a b: got exit code %d, want 1", result.ExitCode)
	}
	if result.Output != "cd: too many arguments" {
		t.Errorf("cd a b: got output %q, want %q", result.Output, "cd: too many arguments")
	}

	// OLDPWD not set
	env.Unset("OLDPWD")
	result = ExecBuiltin(env, "cd -")
	if result.ExitCode != 1 {
		t.Errorf("cd -: got exit code %d, want 1", result.ExitCode)
	}
}

func TestExecBuiltin_CompoundNotIntercepted(t *testing.T) {
	// Compound command starting with cd should NOT be intercepted as builtin
	if IsBuiltin("cd /tmp && ls") {
		t.Error("IsBuiltin should return false for compound commands")
	}
}

func TestNewEnvState(t *testing.T) {
	env := NewEnvState()
	cwd := env.GetCwd()
	expected, _ := os.Getwd()
	if cwd != expected {
		t.Errorf("NewEnvState cwd = %q, want %q", cwd, expected)
	}
}
