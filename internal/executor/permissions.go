// Package executor handles command execution with permission checks.
package executor

import (
	"strings"

	"github.com/anthropics/nsh/internal/config"
	"mvdan.cc/sh/v3/syntax"
)

// PermissionResult is the outcome of evaluating a command against permission rules.
type PermissionResult struct {
	Action      config.PermissionAction
	IsDangerous bool
	Segments    []SegmentResult // per-segment breakdown
}

// SegmentResult holds the evaluation for one segment of a compound command.
type SegmentResult struct {
	Command     string
	Action      config.PermissionAction
	IsDangerous bool
}

// EvaluatePermission parses a command string and evaluates each segment against rules.
// Strictest result wins: deny > ask > allow.
func EvaluatePermission(command string, cfg *config.Config) PermissionResult {
	segments := extractSegments(command, cfg)
	if len(segments) == 0 {
		// Parsing failed or empty → fail safe to ask
		return PermissionResult{
			Action: config.ActionAsk,
			Segments: []SegmentResult{{
				Command: command,
				Action:  config.ActionAsk,
			}},
		}
	}

	result := PermissionResult{
		Action:   config.ActionAllow, // start with least strict
		Segments: segments,
	}

	for _, seg := range segments {
		if seg.IsDangerous {
			result.IsDangerous = true
		}
		if actionStricter(seg.Action, result.Action) {
			result.Action = seg.Action
		}
	}

	return result
}

// extractSegments parses the command and evaluates each segment.
func extractSegments(command string, cfg *config.Config) []SegmentResult {
	parser := syntax.NewParser()
	prog, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		// Fish or unparseable → fall back to first-token matching
		return fallbackSegments(command, cfg)
	}

	var segments []SegmentResult
	seen := make(map[string]bool)

	syntax.Walk(prog, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}

		// Reconstruct the simple command from the call
		cmd := reconstructCall(call)
		if cmd == "" || seen[cmd] {
			return true
		}
		seen[cmd] = true

		action := matchRules(cmd, cfg.AllRules())
		isDangerous := cfg.IsDangerous(firstWord(cmd))
		// Also check multi-word dangerous patterns
		if !isDangerous {
			isDangerous = cfg.IsDangerous(cmd)
		}

		segments = append(segments, SegmentResult{
			Command:     cmd,
			Action:      action,
			IsDangerous: isDangerous,
		})

		return true
	})

	if len(segments) == 0 {
		return fallbackSegments(command, cfg)
	}
	return segments
}

func fallbackSegments(command string, cfg *config.Config) []SegmentResult {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return nil
	}
	action := matchRules(cmd, cfg.AllRules())
	isDangerous := cfg.IsDangerous(firstWord(cmd))
	if !isDangerous {
		isDangerous = cfg.IsDangerous(cmd)
	}
	return []SegmentResult{{
		Command:     cmd,
		Action:      action,
		IsDangerous: isDangerous,
	}}
}

// reconstructCall builds a string from a syntax.CallExpr.
func reconstructCall(call *syntax.CallExpr) string {
	var parts []string
	for _, word := range call.Args {
		var sb strings.Builder
		for _, part := range word.Parts {
			switch p := part.(type) {
			case *syntax.Lit:
				sb.WriteString(p.Value)
			case *syntax.SglQuoted:
				sb.WriteString(p.Value)
			case *syntax.DblQuoted:
				for _, dp := range p.Parts {
					if lit, ok := dp.(*syntax.Lit); ok {
						sb.WriteString(lit.Value)
					}
				}
			default:
				sb.WriteString("*")
			}
		}
		if s := sb.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// matchRules finds the first matching rule. Returns "ask" if no match.
func matchRules(cmd string, rules []config.Rule) config.PermissionAction {
	for _, rule := range rules {
		if matchPattern(rule.Pattern, cmd) {
			return rule.Action
		}
	}
	return config.ActionAsk // default for unknown commands
}

// matchPattern matches a command against a glob-style pattern.
// "*" in the pattern matches any sequence of characters.
func matchPattern(pattern, cmd string) bool {
	// Exact match
	if pattern == cmd {
		return true
	}

	// Use path.Match semantics but allow spaces
	// Replace spaces temporarily to use path.Match
	// Actually, path.Match treats * as matching non-separator chars.
	// We need * to match everything including spaces.
	// Implement simple glob matching.
	return globMatch(pattern, cmd)
}

// globMatch does simple glob matching where * matches any characters (including spaces).
func globMatch(pattern, str string) bool {
	// Use path.Match for patterns without spaces, otherwise manual matching
	if !strings.Contains(pattern, " ") && !strings.Contains(pattern, "*") {
		return pattern == str
	}

	// Simple implementation: split pattern on * and check sequential containment
	parts := strings.Split(pattern, "*")

	if len(parts) == 1 {
		return pattern == str
	}

	// First part must be a prefix
	if !strings.HasPrefix(str, parts[0]) {
		return false
	}
	str = str[len(parts[0]):]

	// Middle parts must appear in order
	for i := 1; i < len(parts)-1; i++ {
		if parts[i] == "" {
			continue
		}
		idx := strings.Index(str, parts[i])
		if idx < 0 {
			return false
		}
		str = str[idx+len(parts[i]):]
	}

	// Last part must be a suffix
	last := parts[len(parts)-1]
	if last == "" {
		return true
	}
	return strings.HasSuffix(str, last)
}

func firstWord(cmd string) string {
	if i := strings.IndexByte(cmd, ' '); i >= 0 {
		return cmd[:i]
	}
	return cmd
}

func actionStricter(a, b config.PermissionAction) bool {
	return actionPriority(a) > actionPriority(b)
}

func actionPriority(a config.PermissionAction) int {
	switch a {
	case config.ActionDeny:
		return 2
	case config.ActionAsk:
		return 1
	case config.ActionAllow:
		return 0
	default:
		return 1 // unknown → treat as ask
	}
}
