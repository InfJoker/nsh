package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// isOOMError checks if an error message indicates a GPU out-of-memory condition.
func isOOMError(errStr string) bool {
	lower := strings.ToLower(errStr)
	return strings.Contains(lower, "compute error") ||
		strings.Contains(lower, "out of memory") ||
		strings.Contains(lower, "outofmemory") ||
		strings.Contains(lower, "kIOGPUCommandBufferCallbackErrorOutOfMemory")
}

// oomUserError returns a user-friendly error for GPU OOM conditions.
func oomUserError() error {
	return fmt.Errorf("model ran out of GPU memory during inference.\n" +
		"Try a smaller quantization (e.g. Q4_K_M instead of Q8_0) or a smaller model.\n" +
		"Run !provider to switch models.")
}

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
		option.WithMiddleware(sseFilterMiddleware),
	)
	return &OpenAICompatProvider{
		client: client,
		model:  model,
	}
}

// sseFilterMiddleware strips SSE comment lines (starting with ":") from streaming
// responses. Some servers (e.g. mlx_lm) send ": keepalive" comments that the
// openai-go SDK doesn't handle, causing "unexpected end of JSON input" errors.
func sseFilterMiddleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp == nil {
		return resp, err
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		return resp, nil
	}
	resp.Body = &sseCommentFilterReader{reader: bufio.NewReader(resp.Body), body: resp.Body}
	return resp, nil
}

// sseCommentFilterReader wraps an SSE stream body, filtering out comment lines
// (lines starting with ":") that some servers emit.
type sseCommentFilterReader struct {
	reader      *bufio.Reader
	body        io.ReadCloser
	buf         bytes.Buffer
	skipComment bool // true after skipping a comment, to also skip the trailing blank line
}

func (r *sseCommentFilterReader) Read(p []byte) (int, error) {
	for r.buf.Len() == 0 {
		line, err := r.reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimLeft(line, " \t")
			if len(trimmed) > 0 && trimmed[0] == ':' {
				// SSE comment — skip it and the blank line that follows
				if err != nil {
					return 0, err
				}
				r.skipComment = true
				continue
			}
			// Skip blank lines immediately after a comment to avoid
			// the SDK interpreting them as empty SSE events.
			if r.skipComment && len(bytes.TrimSpace(line)) == 0 {
				r.skipComment = false
				if err != nil {
					return 0, err
				}
				continue
			}
			r.skipComment = false
			r.buf.Write(line)
		}
		if err != nil {
			if r.buf.Len() > 0 {
				break
			}
			return 0, err
		}
	}
	return r.buf.Read(p)
}

func (r *sseCommentFilterReader) Close() error {
	return r.body.Close()
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
					argsJSON, err := json.Marshal(tc.Arguments)
					if err != nil {
						argsJSON = []byte("{}")
					}
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
		Model:     p.model,
		Messages:  oaiMsgs,
		MaxTokens: openai.Int(4096),
	}

	if len(oaiTools) > 0 {
		params.Tools = oaiTools
	}

	go func() {
		defer close(ch)

		stream := p.client.Chat.Completions.NewStreaming(ctx, params)

		acc := openai.ChatCompletionAccumulator{}
		emittedToolCalls := make(map[string]bool) // keyed by tool call ID

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
				if !emittedToolCalls[tool.ID] {
					if tc := parseToolCall(tool.ID, tool.Name, tool.Arguments); tc != nil {
						emittedToolCalls[tool.ID] = true
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
			reportErr := err
			if isOOMError(err.Error()) {
				reportErr = oomUserError()
			}
			select {
			case ch <- StreamEvent{Type: EventError, Err: reportErr}:
			case <-ctx.Done():
			}
			return
		}

		// Some servers (e.g. Ollama) send entire tool calls in a single chunk,
		// which JustFinishedToolCall() won't detect. Emit any we missed.
		if len(acc.Choices) > 0 {
			for _, tc := range acc.Choices[0].Message.ToolCalls {
				if !emittedToolCalls[tc.ID] && tc.Function.Name != "" {
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
