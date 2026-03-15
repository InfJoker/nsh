package shell

import "strings"

// InputType classifies what kind of input the user provided.
type InputType int

const (
	InputNL      InputType = iota // Natural language → send to agent
	InputDirect                   // ! prefix → run command directly
	InputBuiltin                  // Built-in command (cd, export, etc.)
)

// DispatchResult holds the result of classifying user input.
type DispatchResult struct {
	Type    InputType
	Command string // raw command (without ! prefix)
}

// Dispatch classifies user input and returns the appropriate action.
// Does NOT execute anything — classification only.
func Dispatch(input string) DispatchResult {
	input = strings.TrimSpace(input)
	if input == "" {
		return DispatchResult{Type: InputNL}
	}

	// ! prefix → direct execution
	if strings.HasPrefix(input, "!") {
		cmd := strings.TrimSpace(input[1:])
		if cmd == "" {
			return DispatchResult{Type: InputNL}
		}
		if IsBuiltin(cmd) {
			return DispatchResult{Type: InputBuiltin, Command: cmd}
		}
		return DispatchResult{Type: InputDirect, Command: cmd}
	}

	return DispatchResult{Type: InputNL, Command: input}
}
