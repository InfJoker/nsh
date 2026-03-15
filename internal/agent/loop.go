package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/nsh/internal/config"
	"github.com/anthropics/nsh/internal/executor"
	"github.com/anthropics/nsh/internal/llm"
	"github.com/anthropics/nsh/internal/msgs"
	"github.com/anthropics/nsh/internal/shell"
)

// Agent runs the LLM agent loop in a goroutine.
type Agent struct {
	client    llm.LLMClient
	history   *History
	env       *shell.EnvState
	cfg       *config.Config
	shellPath string
	sendMsg   func(any)
	permFn    executor.PermissionFunc
	projects  *shell.ProjectIndex
}

// SetProjects sets the project index for fuzzy directory matching.
func (a *Agent) SetProjects(p *shell.ProjectIndex) {
	a.projects = p
}

// NewAgent creates a new agent.
func NewAgent(
	client llm.LLMClient,
	history *History,
	env *shell.EnvState,
	cfg *config.Config,
	shellPath string,
	sendMsg func(any),
	permFn executor.PermissionFunc,
) *Agent {
	return &Agent{
		client:    client,
		history:   history,
		env:       env,
		cfg:       cfg,
		shellPath: shellPath,
		sendMsg:   sendMsg,
		permFn:    permFn,
	}
}

// Run processes a user message through the LLM agent loop.
// This should be called in a goroutine. It sends msgs.* to the TUI.
func (a *Agent) Run(ctx context.Context, userInput string) {
	defer func() {
		if r := recover(); r != nil {
			a.sendMsg(msgs.AgentErrorMsg{Err: fmt.Errorf("agent panic: %v", r)})
		}
		a.sendMsg(msgs.AgentDoneMsg{})
	}()

	// Add user message to history
	a.history.Add(llm.Message{Role: "user", Content: userInput})
	a.history.AddInput(userInput)

	tools := ToolDefs()
	te := &ToolExecutor{
		Env:       a.env,
		Config:    a.cfg,
		ShellPath: a.shellPath,
		SendMsg:   a.sendMsg,
		PermFn:    a.permFn,
		Projects:  a.projects,
	}

	// Agent loop: stream → handle tool calls → repeat
	for step := 0; step < a.cfg.MaxSteps; step++ {
		if ctx.Err() != nil {
			return
		}

		// Build messages with system prompt
		systemPrompt := BuildSystemPrompt(a.env, a.shellPath, 0, a.history.Inputs())
		messages := make([]llm.Message, 0, len(a.history.Messages())+1)
		messages = append(messages, llm.Message{Role: "user", Content: systemPrompt})
		messages = append(messages, a.history.Messages()...)

		// Stream from LLM
		stream, err := a.client.Stream(ctx, messages, tools)
		if err != nil {
			a.sendMsg(msgs.AgentErrorMsg{Err: fmt.Errorf("LLM stream error: %w", err)})
			return
		}

		var assistantContent strings.Builder
		var toolCalls []llm.ToolCall

		for event := range stream {
			if ctx.Err() != nil {
				return
			}

			switch event.Type {
			case llm.EventToken:
				a.sendMsg(msgs.TokenMsg{Text: event.Text})
				assistantContent.WriteString(event.Text)

			case llm.EventToolCall:
				if event.ToolCall != nil {
					toolCalls = append(toolCalls, *event.ToolCall)
				}

			case llm.EventError:
				a.sendMsg(msgs.AgentErrorMsg{Err: event.Err})
				return

			case llm.EventDone:
				// Stream finished
			}
		}

		a.sendMsg(msgs.StreamDoneMsg{})

		// Record assistant message
		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   assistantContent.String(),
			ToolCalls: toolCalls,
		}
		a.history.Add(assistantMsg)

		// If no tool calls, we're done
		if len(toolCalls) == 0 {
			return
		}

		// Execute tool calls sequentially
		for _, tc := range toolCalls {
			if ctx.Err() != nil {
				return
			}

			result, err := te.Execute(ctx, tc)
			if err != nil {
				a.sendMsg(msgs.AgentErrorMsg{Err: err})
				return
			}

			// Check for interactive command marker
			if strings.HasPrefix(result, "INTERACTIVE:") {
				// The TUI layer handles this — we signal and wait
				// For now, record the result and continue
				a.history.Add(llm.Message{
					Role:       "tool",
					Content:    "Interactive command launched",
					ToolCallID: tc.ID,
					Name:       tc.Name,
				})
				return
			}

			// Record tool result
			a.history.Add(llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Name,
			})
		}

		// Loop back to get next LLM response with tool results
	}

	a.sendMsg(msgs.AgentErrorMsg{Err: fmt.Errorf("max steps (%d) exceeded", a.cfg.MaxSteps)})
}
