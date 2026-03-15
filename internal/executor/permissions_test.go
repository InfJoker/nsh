package executor

import (
	"testing"

	"github.com/anthropics/nsh/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Permissions: config.Permissions{
			Dangerous: []string{"rm", "sudo", "chmod", "git reset --hard", "git push --force"},
			Rules: []config.Rule{
				{Pattern: "ls *", Action: config.ActionAllow},
				{Pattern: "cat *", Action: config.ActionAllow},
				{Pattern: "echo *", Action: config.ActionAllow},
				{Pattern: "git status", Action: config.ActionAllow},
				{Pattern: "git log *", Action: config.ActionAllow},
				{Pattern: "git push --force *", Action: config.ActionDeny},
				{Pattern: "git reset --hard *", Action: config.ActionDeny},
				{Pattern: "sudo *", Action: config.ActionDeny},
				{Pattern: "rm -rf /*", Action: config.ActionDeny},
			},
		},
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		cmd     string
		want    bool
	}{
		{"ls *", "ls -la", true},
		{"ls *", "ls", false},
		{"git status", "git status", true},
		{"git status", "git status --short", false},
		{"git log *", "git log --oneline", true},
		{"echo *", "echo hello world", true},
		{"sudo *", "sudo rm -rf /", true},
		{"rm -rf /*", "rm -rf /tmp", true},
		{"cat *", "cat foo.txt", true},
	}

	for _, tt := range tests {
		got := globMatch(tt.pattern, tt.cmd)
		if got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.cmd, got, tt.want)
		}
	}
}

func TestEvaluatePermission_SimpleCommands(t *testing.T) {
	cfg := testConfig()

	tests := []struct {
		command    string
		wantAction config.PermissionAction
	}{
		{"ls -la", config.ActionAllow},
		{"cat foo.txt", config.ActionAllow},
		{"git status", config.ActionAllow},
		{"git log --oneline", config.ActionAllow},
		{"sudo rm -rf /", config.ActionDeny},
		{"git push --force origin main", config.ActionDeny},
		{"git reset --hard HEAD~1", config.ActionDeny},
		{"curl example.com", config.ActionAsk}, // unknown command
	}

	for _, tt := range tests {
		result := EvaluatePermission(tt.command, cfg)
		if result.Action != tt.wantAction {
			t.Errorf("EvaluatePermission(%q) action = %v, want %v", tt.command, result.Action, tt.wantAction)
		}
	}
}

func TestEvaluatePermission_PipeChain(t *testing.T) {
	cfg := testConfig()

	// Pipe: ls | grep foo → both allowed
	result := EvaluatePermission("ls -la | grep foo", cfg)
	// grep is not in allow list, so strictest = ask
	if result.Action != config.ActionAsk {
		t.Errorf("pipe with unknown command: got %v, want ask", result.Action)
	}

	// ls alone → allowed
	result = EvaluatePermission("ls -la", cfg)
	if result.Action != config.ActionAllow {
		t.Errorf("ls -la: got %v, want allow", result.Action)
	}
}

func TestEvaluatePermission_DangerousHighlight(t *testing.T) {
	cfg := testConfig()

	result := EvaluatePermission("rm -rf /tmp/test", cfg)
	if !result.IsDangerous {
		t.Error("rm command should be marked as dangerous")
	}

	result = EvaluatePermission("ls -la", cfg)
	if result.IsDangerous {
		t.Error("ls command should not be marked as dangerous")
	}
}

func TestEvaluatePermission_StrictestWins(t *testing.T) {
	cfg := testConfig()

	// echo allowed && sudo denied → deny wins
	result := EvaluatePermission("echo hello && sudo rm -rf /", cfg)
	if result.Action != config.ActionDeny {
		t.Errorf("deny should win in chain: got %v, want deny", result.Action)
	}
}

func TestFirstWord(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ls -la", "ls"},
		{"git status", "git"},
		{"echo", "echo"},
		{"", ""},
	}

	for _, tt := range tests {
		got := firstWord(tt.input)
		if got != tt.want {
			t.Errorf("firstWord(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

