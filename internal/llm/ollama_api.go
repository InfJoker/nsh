package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// OllamaAPIProvider implements LLMClient using the native Ollama /api/chat
// endpoint with NDJSON streaming. Used by Hypura (which only speaks Ollama
// protocol) and potentially useful for native Ollama communication.
type OllamaAPIProvider struct {
	model   string
	baseURL string // e.g. "http://127.0.0.1:8080"
}

// NewOllamaAPIProvider creates a provider targeting an Ollama-compatible API.
func NewOllamaAPIProvider(model, baseURL string) *OllamaAPIProvider {
	return &OllamaAPIProvider{model: model, baseURL: baseURL}
}

// ollamaChatRequest is the POST body for /api/chat.
type ollamaChatRequest struct {
	Model    string             `json:"model"`
	Messages []ollamaChatMsg    `json:"messages"`
	Stream   bool               `json:"stream"`
	Tools    []ollamaChatTool   `json:"tools,omitempty"`
}

type ollamaChatMsg struct {
	Role      string              `json:"role"`
	Content   string              `json:"content"`
	ToolCalls []ollamaToolCall    `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolCallFunc `json:"function"`
}

type ollamaToolCallFunc struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

type ollamaChatTool struct {
	Type     string              `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ollamaChatResponse is a single NDJSON line from /api/chat streaming.
type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		ToolCalls []struct {
			Function struct {
				Name      string `json:"name"`
				Arguments any    `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	} `json:"message"`
	Done bool `json:"done"`
}

func (p *OllamaAPIProvider) Stream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 32)

	// Convert messages
	var ollamaMsgs []ollamaChatMsg
	for i, msg := range messages {
		switch msg.Role {
		case "user":
			role := "user"
			if i == 0 {
				role = "system"
			}
			ollamaMsgs = append(ollamaMsgs, ollamaChatMsg{Role: role, Content: msg.Content})
		case "assistant":
			m := ollamaChatMsg{Role: "assistant", Content: msg.Content}
			for _, tc := range msg.ToolCalls {
				m.ToolCalls = append(m.ToolCalls, ollamaToolCall{
					Function: ollamaToolCallFunc{Name: tc.Name, Arguments: tc.Arguments},
				})
			}
			ollamaMsgs = append(ollamaMsgs, m)
		case "tool":
			ollamaMsgs = append(ollamaMsgs, ollamaChatMsg{Role: "tool", Content: msg.Content})
		}
	}

	// Convert tools
	var ollamaTools []ollamaChatTool
	for _, t := range tools {
		ollamaTools = append(ollamaTools, ollamaChatTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	reqBody := ollamaChatRequest{
		Model:    p.model,
		Messages: ollamaMsgs,
		Stream:   true,
		Tools:    ollamaTools,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posting to %s/api/chat: %w", p.baseURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("ollama API returned %d: %s", resp.StatusCode, string(errBody))
	}

	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024) // 1MB max line
		toolCallIdx := 0

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var chunk ollamaChatResponse
			if err := json.Unmarshal(line, &chunk); err != nil {
				select {
				case ch <- StreamEvent{Type: EventError, Err: fmt.Errorf("parsing NDJSON: %w", err)}:
				case <-ctx.Done():
				}
				return
			}

			if chunk.Done {
				select {
				case ch <- StreamEvent{Type: EventDone}:
				case <-ctx.Done():
				}
				return
			}

			// Text token
			if chunk.Message.Content != "" {
				select {
				case ch <- StreamEvent{Type: EventToken, Text: chunk.Message.Content}:
				case <-ctx.Done():
					return
				}
			}

			// Tool calls
			for _, tc := range chunk.Message.ToolCalls {
				args := parseOllamaToolArgs(tc.Function.Arguments)
				id := fmt.Sprintf("call_%d", toolCallIdx)
				toolCallIdx++
				select {
				case ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCall{
					ID:        id,
					Name:      tc.Function.Name,
					Arguments: args,
				}}:
				case <-ctx.Done():
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			if ctx.Err() != nil {
				return
			}
			errMsg := err.Error()
			if isOOMError(errMsg) {
				select {
				case ch <- StreamEvent{Type: EventError, Err: oomUserError()}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case ch <- StreamEvent{Type: EventError, Err: fmt.Errorf("reading stream: %w", err)}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

// parseOllamaToolArgs converts Ollama's tool call arguments (which can be
// map[string]any from JSON) into the map[string]string the agent expects.
func parseOllamaToolArgs(raw any) map[string]string {
	args := make(map[string]string)
	switch v := raw.(type) {
	case map[string]any:
		for k, val := range v {
			args[k] = fmt.Sprintf("%v", val)
		}
	case map[string]string:
		return v
	case string:
		// Try to parse as JSON
		var parsed map[string]any
		if json.Unmarshal([]byte(v), &parsed) == nil {
			for k, val := range parsed {
				args[k] = fmt.Sprintf("%v", val)
			}
		}
	}
	return args
}
