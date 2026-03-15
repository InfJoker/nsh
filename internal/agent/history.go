// Package agent implements the LLM agent loop and tool execution.
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/InfJoker/nsh/internal/config"
	"github.com/InfJoker/nsh/internal/llm"
)

const maxConversationMessages = 100 // sliding window to avoid context overflow

// History manages conversation history and input history.
type History struct {
	mu       sync.Mutex
	messages []llm.Message
	inputs   []string // NL input history for up/down recall
	maxInput int
}

// NewHistory creates a new conversation history manager.
func NewHistory() *History {
	return &History{
		maxInput: 1000,
	}
}

// Add appends a message to conversation history, trimming old messages if over limit.
func (h *History) Add(msg llm.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, msg)
	if len(h.messages) > maxConversationMessages {
		// Keep the most recent messages, trim from the front
		h.messages = h.messages[len(h.messages)-maxConversationMessages:]
	}
}

// Messages returns a copy of the conversation history.
func (h *History) Messages() []llm.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]llm.Message, len(h.messages))
	copy(result, h.messages)
	return result
}

// Clear resets conversation history (but keeps input history).
func (h *History) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = nil
}

// AddInput records a user NL input for history recall.
func (h *History) AddInput(input string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Deduplicate: remove if already exists
	for i, s := range h.inputs {
		if s == input {
			h.inputs = append(h.inputs[:i], h.inputs[i+1:]...)
			break
		}
	}

	h.inputs = append(h.inputs, input)

	// Trim to max
	if len(h.inputs) > h.maxInput {
		h.inputs = h.inputs[len(h.inputs)-h.maxInput:]
	}
}

// Inputs returns the input history (oldest first).
func (h *History) Inputs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]string, len(h.inputs))
	copy(result, h.inputs)
	return result
}

// LoadInputHistory loads input history from disk.
func (h *History) LoadInputHistory() {
	path := filepath.Join(config.DataDir(), "history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = json.Unmarshal(data, &h.inputs)
}

// SaveInputHistory persists input history to disk.
func (h *History) SaveInputHistory() error {
	h.mu.Lock()
	data, err := json.Marshal(h.inputs)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	path := filepath.Join(config.DataDir(), "history.json")
	return os.WriteFile(path, data, 0644)
}
