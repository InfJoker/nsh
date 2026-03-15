package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// AnthropicProvider implements LLMClient using the Anthropic SDK.
type AnthropicProvider struct {
	client anthropic.Client
	model  string
}

// NewAnthropicProvider creates a provider using the Anthropic API.
func NewAnthropicProvider(model string) (*AnthropicProvider, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	return &AnthropicProvider{
		client: client,
		model:  model,
	}, nil
}

func (p *AnthropicProvider) Stream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 32)

	// Convert messages — first "user" message is the system prompt
	var systemPrompt string
	var anthropicMsgs []anthropic.MessageParam

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if len(anthropicMsgs) == 0 && systemPrompt == "" {
				systemPrompt = msg.Content
				continue
			}
			anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
				anthropic.NewTextBlock(msg.Content),
			))
		case "assistant":
			blocks := []anthropic.ContentBlockParamUnion{
				anthropic.NewTextBlock(msg.Content),
			}
			for _, tc := range msg.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Arguments)
				blocks = append(blocks, anthropic.ContentBlockParamOfRequestToolUseBlock(
					tc.ID, json.RawMessage(argsJSON), tc.Name,
				))
			}
			anthropicMsgs = append(anthropicMsgs, anthropic.NewAssistantMessage(blocks...))
		case "tool":
			anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false),
			))
		}
	}

	// Convert tools
	var anthropicTools []anthropic.ToolUnionParam
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{
			Properties: t.Parameters["properties"],
		}
		if req, ok := t.Parameters["required"]; ok {
			schema.ExtraFields = map[string]interface{}{
				"required": req,
			}
		}
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: schema,
			},
		})
	}

	params := anthropic.MessageNewParams{
		Model:     p.model,
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
	}

	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	if len(anthropicTools) > 0 {
		params.Tools = anthropicTools
	}

	go func() {
		defer close(ch)

		stream := p.client.Messages.NewStreaming(ctx, params)
		defer stream.Close()

		// Track tool call accumulation: index → {id, name, accumulated JSON}
		type toolCallState struct {
			id      string
			name    string
			argsJSON string
		}
		toolCalls := make(map[int]*toolCallState)

		for stream.Next() {
			event := stream.Current()

			switch e := event.AsAny().(type) {
			case anthropic.ContentBlockStartEvent:
				switch block := e.ContentBlock.AsAny().(type) {
				case anthropic.ToolUseBlock:
					toolCalls[int(e.Index)] = &toolCallState{
						id:   block.ID,
						name: block.Name,
					}
				}

			case anthropic.ContentBlockDeltaEvent:
				switch delta := e.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					select {
					case ch <- StreamEvent{Type: EventToken, Text: delta.Text}:
					case <-ctx.Done():
						return
					}
				case anthropic.InputJSONDelta:
					idx := int(e.Index)
					if tc, ok := toolCalls[idx]; ok {
						tc.argsJSON += delta.PartialJSON
					}
				}

			case anthropic.ContentBlockStopEvent:
				idx := int(e.Index)
				if tc, ok := toolCalls[idx]; ok {
					var args map[string]string
					if err := json.Unmarshal([]byte(tc.argsJSON), &args); err != nil {
						// Try as map[string]any and stringify
						var anyArgs map[string]any
						if err2 := json.Unmarshal([]byte(tc.argsJSON), &anyArgs); err2 == nil {
							args = make(map[string]string)
							for k, v := range anyArgs {
								args[k] = fmt.Sprintf("%v", v)
							}
						}
					}

					select {
					case ch <- StreamEvent{Type: EventToolCall, ToolCall: &ToolCall{
						ID:        tc.id,
						Name:      tc.name,
						Arguments: args,
					}}:
					case <-ctx.Done():
						return
					}
					delete(toolCalls, idx)
				}
			}
		}

		if err := stream.Err(); err != nil {
			select {
			case ch <- StreamEvent{Type: EventError, Err: err}:
			case <-ctx.Done():
			}
			return
		}

		select {
		case ch <- StreamEvent{Type: EventDone}:
		case <-ctx.Done():
		}
	}()

	return ch, nil
}

// NewProvider creates an LLMClient based on provider name, model, and optional base URL.
func NewProvider(provider, model, baseURL string) (LLMClient, error) {
	switch provider {
	case "anthropic":
		return NewAnthropicProvider(model)
	case "copilot":
		return nil, fmt.Errorf("copilot provider not yet implemented — use provider = \"anthropic\" with ANTHROPIC_API_KEY")
	case "ollama":
		if baseURL == "" {
			baseURL = "http://localhost:11434/v1"
		}
		return NewOpenAICompatProvider(model, baseURL, "nsh"), nil
	case "mock":
		return NewMockClient(), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %q (supported: anthropic, copilot, ollama)", provider)
	}
}
