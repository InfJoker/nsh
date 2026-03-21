package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anthropics/nsh/internal/config"
	"github.com/anthropics/nsh/internal/executor"
	"github.com/anthropics/nsh/internal/llm"
	"github.com/anthropics/nsh/internal/msgs"
	"github.com/anthropics/nsh/internal/shell"
)

const maxReadSize = 256 * 1024 // 256KB

// multiWordCommands are commands whose second token is semantically important
// for permission pattern generation.
var multiWordCommands = map[string]bool{
	"git": true, "npm": true, "docker": true, "kubectl": true,
	"go": true, "cargo": true, "pip": true, "yarn": true,
}

// ToolDefs returns the tool definitions for the LLM.
func ToolDefs() []llm.Tool {
	return []llm.Tool{
		{
			Name:        "run_command",
			Description: "Execute a shell command and capture its output. Use when you need to read the result (ls, grep, cat, git status, build/test commands). Do NOT use for programs that may need user input — use launch_interactive instead.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The shell command to execute",
					},
					"timeout_seconds": map[string]any{
						"type":        "integer",
						"description": "Optional timeout in seconds (default 30)",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "launch_interactive",
			Description: "Launch a program with full terminal access. Use for interactive programs (vim, ssh, python, node), unfamiliar commands, or anything that might need user input. When unsure whether a command is interactive, use this.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The interactive command to launch",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "change_directory",
			Description: "Change the working directory. Supports absolute paths, relative paths, ~ expansion, and fuzzy project name matching.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Directory path or project name to cd into",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "read_file",
			Description: "Read the contents of a file. Paths outside the current directory require permission. Max 256KB.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to read",
					},
					"start_line": map[string]any{
						"type":        "integer",
						"description": "Optional start line (1-indexed)",
					},
					"end_line": map[string]any{
						"type":        "integer",
						"description": "Optional end line (1-indexed, inclusive)",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

// ToolExecutor handles tool call execution.
type ToolExecutor struct {
	Env       *shell.EnvState
	Config    *config.Config
	ShellPath string
	SendMsg   func(any) // sends msgs.* to bubbletea
	PermFn    executor.PermissionFunc
	Projects  *shell.ProjectIndex
}

// Execute runs a tool call and returns the result string.
func (te *ToolExecutor) Execute(ctx context.Context, tc llm.ToolCall) (string, error) {
	switch tc.Name {
	case "run_command":
		return te.execRunCommand(ctx, tc)
	case "launch_interactive":
		return te.execLaunchInteractive(ctx, tc)
	case "change_directory":
		return te.execChangeDirectory(ctx, tc)
	case "read_file":
		return te.execReadFile(ctx, tc)
	default:
		return "", fmt.Errorf("unknown tool: %s", tc.Name)
	}
}

func (te *ToolExecutor) execRunCommand(ctx context.Context, tc llm.ToolCall) (string, error) {
	command := tc.Arguments["command"]
	if command == "" {
		return "", fmt.Errorf("run_command: missing command argument")
	}

	// Parse optional timeout
	var timeoutSec int
	if ts := tc.Arguments["timeout_seconds"]; ts != "" {
		if v, err := strconv.Atoi(ts); err == nil && v > 0 {
			timeoutSec = v
		}
	}

	// Check permissions
	perm := executor.EvaluatePermission(command, te.Config)

	te.SendMsg(msgs.ToolCallStartMsg{Name: "run_command", Desc: command})

	if perm.Action == config.ActionDeny {
		result := fmt.Sprintf("Command denied by permission rules: %s", command)
		te.SendMsg(msgs.ToolCallDoneMsg{Name: "run_command", Result: result})
		return result, nil
	}

	if perm.Action == config.ActionAsk {
		resp, err := te.PermFn(ctx, command, perm.IsDangerous)
		if err != nil {
			return "", err
		}
		switch resp {
		case msgs.PermissionDeny:
			result := "Command denied by user"
			te.SendMsg(msgs.ToolCallDoneMsg{Name: "run_command", Result: result})
			return result, nil
		case msgs.PermissionAlways:
			pattern := GeneratePattern(command)
			_ = te.Config.AppendLearnedRule(config.Rule{Pattern: pattern, Action: config.ActionAllow})
		case msgs.PermissionOnce:
			// proceed
		}
	}

	var timeout = executor.DefaultTimeout
	if timeoutSec > 0 {
		timeout = executor.TimeoutFromSeconds(timeoutSec)
	}

	var output strings.Builder
	exitCode, err := executor.RunCaptured(ctx, command, te.Env, te.ShellPath, func(msg msgs.CommandOutputMsg) {
		te.SendMsg(msg)
		if msg.IsStderr {
			fmt.Fprintf(&output, "[stderr] %s\n", msg.Line)
		} else {
			fmt.Fprintf(&output, "%s\n", msg.Line)
		}
	}, timeout)

	if err != nil {
		result := fmt.Sprintf("<command_output cmd=%q>\nError: %v\n</command_output>", command, err)
		te.SendMsg(msgs.ToolCallDoneMsg{Name: "run_command", Result: result})
		return result, nil
	}

	result := fmt.Sprintf("<command_output cmd=%q exit_code=%d>\n%s</command_output>", command, exitCode, output.String())
	te.SendMsg(msgs.ToolCallDoneMsg{Name: "run_command", Result: result})
	return result, nil
}

func (te *ToolExecutor) execLaunchInteractive(_ context.Context, tc llm.ToolCall) (string, error) {
	command := tc.Arguments["command"]
	if command == "" {
		return "", fmt.Errorf("launch_interactive: missing command argument")
	}

	// Pre-check: verify the command binary exists before launching.
	// launch_interactive uses tea.ExecProcess which gives full terminal control —
	// if the command doesn't exist, the shell error prints outside the TUI
	// and corrupts the display. Catch it here and return as a normal tool result.
	bin := extractBinary(command)
	if bin != "" {
		if _, err := exec.LookPath(bin); err != nil {
			result := fmt.Sprintf("command not found: %s", bin)
			// Use "run_command" name so the TUI renders a normal command card,
			// NOT "launch_interactive" which would trigger tea.ExecProcess.
			te.SendMsg(msgs.ToolCallStartMsg{Name: "run_command", Desc: command})
			te.SendMsg(msgs.ToolCallDoneMsg{Name: "run_command", Result: result})
			return result, nil
		}
	}

	te.SendMsg(msgs.ToolCallStartMsg{Name: "launch_interactive", Desc: command})
	return fmt.Sprintf("INTERACTIVE:%s", command), nil
}

func (te *ToolExecutor) execChangeDirectory(_ context.Context, tc llm.ToolCall) (string, error) {
	path := tc.Arguments["path"]
	if path == "" {
		return "", fmt.Errorf("change_directory: missing path argument")
	}

	te.SendMsg(msgs.ToolCallStartMsg{Name: "change_directory", Desc: path})

	// Try fuzzy project match if path doesn't exist as-is
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, ".") && !strings.HasPrefix(path, "~") {
		resolved := filepath.Join(te.Env.GetCwd(), path)
		if _, err := os.Stat(resolved); os.IsNotExist(err) && te.Projects != nil {
			if match := te.Projects.FindProject(path); match != "" {
				path = match
			}
		}
	}

	result := shell.ExecBuiltin(te.Env, "cd "+path)
	if result.Output != "" {
		te.SendMsg(msgs.ToolCallDoneMsg{Name: "change_directory", Result: result.Output})
		return result.Output, nil
	}

	if result.NewCwd != "" {
		te.SendMsg(msgs.CwdChangedMsg{Path: result.NewCwd})
		if te.Projects != nil {
			te.Projects.Record(result.NewCwd)
		}
	}

	msg := fmt.Sprintf("Changed directory to %s", te.Env.GetCwd())
	te.SendMsg(msgs.ToolCallDoneMsg{Name: "change_directory", Result: msg})
	return msg, nil
}

func (te *ToolExecutor) execReadFile(ctx context.Context, tc llm.ToolCall) (string, error) {
	filePath := tc.Arguments["path"]
	if filePath == "" {
		return "", fmt.Errorf("read_file: missing path argument")
	}

	// Resolve relative to cwd
	cwd := te.Env.GetCwd()
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwd, filePath)
	}
	filePath = filepath.Clean(filePath)

	te.SendMsg(msgs.ToolCallStartMsg{Name: "read_file", Desc: filePath})

	// Check if outside cwd → permission prompt
	// Use cwd+"/" to prevent prefix collision (e.g., /app matching /app-secrets)
	if filePath != cwd && !strings.HasPrefix(filePath, cwd+string(os.PathSeparator)) {
		resp, err := te.PermFn(ctx, "read_file: "+filePath, false)
		if err != nil {
			return "", err
		}
		if resp == msgs.PermissionDeny {
			result := "File read denied by user"
			te.SendMsg(msgs.ToolCallDoneMsg{Name: "read_file", Result: result})
			return result, nil
		}
	}

	// Check file size
	info, err := os.Stat(filePath)
	if err != nil {
		result := fmt.Sprintf("Error reading file: %v", err)
		te.SendMsg(msgs.ToolCallDoneMsg{Name: "read_file", Result: result})
		return result, nil
	}
	if info.Size() > maxReadSize {
		result := fmt.Sprintf("File too large (%d bytes, max %d). Use start_line/end_line to read a portion.", info.Size(), maxReadSize)
		te.SendMsg(msgs.ToolCallDoneMsg{Name: "read_file", Result: result})
		return result, nil
	}

	// Parse optional line range
	startLine, _ := strconv.Atoi(tc.Arguments["start_line"])
	endLine, _ := strconv.Atoi(tc.Arguments["end_line"])

	var content string
	if startLine > 0 || endLine > 0 {
		content, err = readLineRange(filePath, startLine, endLine)
	} else {
		var data []byte
		data, err = os.ReadFile(filePath)
		content = string(data)
	}

	if err != nil {
		result := fmt.Sprintf("Error reading file: %v", err)
		te.SendMsg(msgs.ToolCallDoneMsg{Name: "read_file", Result: result})
		return result, nil
	}

	result := fmt.Sprintf("<file_content path=%q>\n%s\n</file_content>", filePath, content)
	te.SendMsg(msgs.ToolCallDoneMsg{Name: "read_file", Result: result})
	return result, nil
}

func readLineRange(path string, start, end int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if start < 1 {
		start = 1
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < start {
			continue
		}
		if end > 0 && lineNum > end {
			break
		}
		fmt.Fprintf(&sb, "%d\t%s\n", lineNum, scanner.Text())
	}
	return sb.String(), scanner.Err()
}

// GeneratePattern creates a permission pattern from a concrete command.
// For multi-word commands (git, docker, npm, etc.), keeps the first two tokens.
// For single-word commands, keeps the command and wildcards the rest.
func GeneratePattern(command string) string {
	parts := strings.Fields(command)
	if len(parts) <= 1 {
		return command
	}

	// For known multi-word commands, keep 2 tokens
	if multiWordCommands[parts[0]] && len(parts) >= 2 {
		if len(parts) == 2 {
			// Exact 2-token command: "git status" → "git status" (no wildcard)
			return parts[0] + " " + parts[1]
		}
		return parts[0] + " " + parts[1] + " *"
	}

	return parts[0] + " *"
}

// extractBinary returns the first token in a shell command that isn't
// an env var assignment (KEY=VAL). Returns "" if no binary found.
func extractBinary(command string) string {
	for _, tok := range strings.Fields(command) {
		// Skip env var assignments: contains '=' and doesn't start with - or /
		if strings.Contains(tok, "=") && tok[0] != '-' && tok[0] != '/' {
			continue
		}
		return tok
	}
	return ""
}
