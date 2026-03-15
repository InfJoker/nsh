// Package llm defines domain types and the client interface for LLM providers.
package llm

// Message represents a conversation message.
type Message struct {
	Role       string // "user", "assistant", "tool"
	Content    string
	ToolCallID string // for tool result messages
	Name       string // tool name for tool results
	ToolCalls  []ToolCall
}

// Tool defines a tool the LLM can invoke.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema as map
}

// ToolCall represents a tool invocation from the LLM.
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]string // parsed key-value args
}
