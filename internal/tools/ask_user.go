package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-v4/internal/agent"
)

// AskUser blocks until the user supplies an answer via AskFn, which the
// CLI implements as a simple stdin readline. The tool itself knows nothing
// about stdin — AskFn is the seam that lets a future web API (or test)
// implement the same interaction differently without touching this code.
//
// Subagents must NOT get this tool: delegate_task's contract is a fully-
// specified, self-contained subtask. If a subtask is ambiguous enough to
// need user input it wasn't well-specified for delegation. Practically,
// multiple concurrent subagents sharing a single stdin channel would race
// each other.
type AskUser struct {
	// AskFn is the actual blocking I/O. It receives the question and
	// returns the user's answer. The CLI uses bufio.NewReader(os.Stdin).
	AskFn func(question string) (answer string, err error)
}

func (AskUser) Name() string { return "ask_user" }

// Mode is Exclusive: blocks the entire process; must never run alongside
// anything else in the same step's concurrent batch.
func (AskUser) Mode() agent.ToolMode { return agent.Exclusive }

func (AskUser) Description() string {
	return "Ask the user one focused clarifying question and wait for their answer. " +
		"Use this when the task is genuinely ambiguous and the answer would meaningfully " +
		"change what you build — not for minor style choices. Prefer a stated reasonable " +
		"assumption over asking about trivialities. Do not call this in delegated subtasks."
}

func (AskUser) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"question": {"type": "string"}
		},
		"required": ["question"]
	}`)
}

func (t AskUser) Run(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	if t.AskFn == nil {
		return "", fmt.Errorf("ask_user: no AskFn configured")
	}
	answer, err := t.AskFn(in.Question)
	if err != nil {
		return "", fmt.Errorf("ask_user: %w", err)
	}
	return answer, nil
}
