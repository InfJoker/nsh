package llm

import (
	"context"
	"time"
)

// MockClient is a mock LLM client for testing.
type MockClient struct {
	// Responses to return in order. Each inner slice is one stream.
	Responses [][]StreamEvent
	callIdx   int
}

// NewMockClient creates a mock client with a simple echo response.
func NewMockClient() *MockClient {
	return &MockClient{
		Responses: [][]StreamEvent{
			{
				{Type: EventToken, Text: "I'll help you with that. Let me "},
				{Type: EventToken, Text: "run the command."},
				{Type: EventDone},
			},
		},
	}
}

func (m *MockClient) Stream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 16)

	// Pick response
	var events []StreamEvent
	if m.callIdx < len(m.Responses) {
		events = m.Responses[m.callIdx]
		m.callIdx++
	} else {
		events = []StreamEvent{
			{Type: EventToken, Text: "Done."},
			{Type: EventDone},
		}
	}

	go func() {
		defer close(ch)
		for _, event := range events {
			select {
			case <-ctx.Done():
				return
			case ch <- event:
				// Simulate streaming delay
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	return ch, nil
}
