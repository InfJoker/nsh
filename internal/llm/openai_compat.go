package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// OpenAICompatProvider implements LLMClient for any OpenAI-compatible API.
// Used by Ollama now; reusable for OpenRouter, Groq, etc.
type OpenAICompatProvider struct {
	client openai.Client
	model  string
}

// NewOpenAICompatProvider creates a provider targeting any OpenAI-compatible endpoint.
func NewOpenAICompatProvider(model, baseURL, apiKey string) *OpenAICompatProvider {
	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
	)
	return &OpenAICompatProvider{
		client: client,
		model:  model,
	}
}

// parseToolCall parses a tool call's JSON arguments into a ToolCall.
func parseToolCall(id, name, arguments string) *ToolCall {
	var args map[string]string
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		var anyArgs map[string]any
		if err2 := json.Unmarshal([]byte(arguments), &anyArgs); err2 == nil {
			args = make(map[string]string)
			for k, v := range anyArgs {
				args[k] = fmt.Sprintf("%v", v)
			}
		}
	}
	return &ToolCall{
		ID:        id,
		Name:      name,
		Arguments: args,
	}
}

func (p *OpenAICompatProvider) Stream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 32)

	// Convert messages — first "user" message is the system prompt
	var oaiMsgs []openai.ChatCompletionMessageParamUnion

	for i, msg := range messages {
		switch msg.Role {
		case "user":
			if i == 0 {
				// First user message is system prompt
				oaiMsgs = append(oaiMsgs, openai.SystemMessage(msg.Content))
				continue
			}
			oaiMsgs = append(oaiMsgs, openai.UserMessage(msg.Content))
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				var toolCalls []openai.ChatCompletionMessageToolCallUnionParam
				for _, tc := range msg.ToolCalls {
					argsJSON, _ := json.Marshal(tc.Arguments)
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID: tc.ID,
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Name,
								Arguments: string(argsJSON),
							},
						},
					})
				}
				oaiMsgs = append(oaiMsgs, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content: openai.ChatCompletionAssistantMessageParamContentUnion{
							OfString: openai.String(msg.Content),
						},
						ToolCalls: toolCalls,
					},
				})
			} else {
				oaiMsgs = append(oaiMsgs, openai.AssistantMessage(msg.Content))
			}
		case "tool":
			oaiMsgs = append(oaiMsgs, openai.ToolMessage(msg.Content, msg.ToolCallID))
		}
	}

	// Convert tools
	var oaiTools []openai.ChatCompletionToolUnionParam
	for _, t := range tools {
		oaiTools = append(oaiTools, openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  openai.FunctionParameters(t.Parameters),
		}))
	}

	params := openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: oaiMsgs,
	}

	if len(oaiTools) > 0 {
		params.Tools = oaiTools
	}

	go func() {
		defer close(ch)

		stream := p.client.Chat.Completions.NewStreaming(ctx, params)

		acc := openai.ChatCompletionAccumulator{}
		emittedToolCalls := make(map[int]bool)

		for stream.Next() {
			chunk := stream.Current()
			acc.AddChunk(chunk)

			// Stream text tokens
			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta
				if delta.Content != "" {
					select {
					case ch <- StreamEvent{Type: EventToken, Text: delta.Content}:
					case <-ctx.Done():
						return
					}
				}
			}

			// Detect completed tool calls via accumulator
			if tool, ok := acc.JustFinishedToolCall(); ok {
				if !emittedToolCalls[len(emittedToolCalls)] {
					if tc := parseToolCall(tool.ID, tool.Name, tool.Arguments); tc != nil {
						emittedToolCalls[len(emittedToolCalls)] = true
						select {
						case ch <- StreamEvent{Type: EventToolCall, ToolCall: tc}:
						case <-ctx.Done():
							return
						}
					}
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

		// Ollama may send entire tool calls in a single chunk, which the
		// accumulator's JustFinishedToolCall() won't detect. After the
		// stream ends, emit any accumulated tool calls we haven't sent yet.
		if len(acc.Choices) > 0 {
			for i, tc := range acc.Choices[0].Message.ToolCalls {
				if !emittedToolCalls[i] && tc.Function.Name != "" {
					if parsed := parseToolCall(tc.ID, tc.Function.Name, tc.Function.Arguments); parsed != nil {
						select {
						case ch <- StreamEvent{Type: EventToolCall, ToolCall: parsed}:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}

		select {
		case ch <- StreamEvent{Type: EventDone}:
		case <-ctx.Done():
		}
	}()

	return ch, nil
}
