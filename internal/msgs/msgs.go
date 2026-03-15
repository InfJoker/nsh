// Package msgs defines the shared message contract between agent and TUI.
package msgs

// TokenMsg carries a streaming text chunk from the LLM.
type TokenMsg struct{ Text string }

// StreamDoneMsg signals the LLM stream has finished.
type StreamDoneMsg struct{}

// ToolCallStartMsg signals the agent is invoking a tool.
type ToolCallStartMsg struct{ Name, Desc string }

// ToolCallDoneMsg signals tool execution completed.
type ToolCallDoneMsg struct{ Name, Result string }

// CommandOutputMsg carries a line of command output.
type CommandOutputMsg struct {
	Line     string
	IsStderr bool
}

// AgentErrorMsg signals an error in the agent loop.
type AgentErrorMsg struct{ Err error }

// AgentDoneMsg signals the agent has finished processing a request.
type AgentDoneMsg struct{}

// CwdChangedMsg signals the agent mutated the working directory.
type CwdChangedMsg struct{ Path string }

// PermissionResponse represents the user's response to a permission prompt.
type PermissionResponse int

const (
	PermissionDeny   PermissionResponse = iota
	PermissionOnce                      // run this time only
	PermissionAlways                    // run + add to learned_rules.toml
)

// PermissionRequestMsg is sent from the agent to the TUI when a command needs approval.
// The agent blocks on ResponseCh until the TUI writes a response.
type PermissionRequestMsg struct {
	Command    string
	IsDangerous bool
	ResponseCh chan<- PermissionResponse
}

// InteractiveDoneMsg signals an interactive process (vim, ssh, etc.) has exited.
type InteractiveDoneMsg struct{ Err error }

// CancelMsg signals the user pressed Ctrl+C.
type CancelMsg struct{}
