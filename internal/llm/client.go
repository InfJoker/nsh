package llm

import "context"

// StreamEventType identifies the kind of streaming event.
type StreamEventType int

const (
	EventToken    StreamEventType = iota // Text token
	EventToolCall                        // Complete tool call
	EventDone                            // Stream finished
	EventError                           // Error occurred
)

// StreamEvent is a single event from an LLM stream.
type StreamEvent struct {
	Type     StreamEventType
	Text     string    // for EventToken
	ToolCall *ToolCall // for EventToolCall
	Err      error     // for EventError
}

// LLMClient streams completions from an LLM provider.
type LLMClient interface {
	Stream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error)
}
