package agent

import (
	"context"
	"encoding/json"
	"testing"

	"tars/internal/llm"
)

// errStub always fails, so every step it appears in is unproductive.
type errStub struct{ name string }

func (e errStub) Name() string          { return e.name }
func (errStub) Description() string     { return "stub" }
func (errStub) Mode() ToolMode          { return Concurrent }
func (errStub) Schema() json.RawMessage { return json.RawMessage(`{}`) }
func (errStub) Run(context.Context, json.RawMessage) (string, error) {
	return "", context.DeadlineExceeded
}

func failStep(id, path string) llm.ChatResponse {
	args, _ := json.Marshal(map[string]string{"path": path})
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: id, Name: "flaky", Arguments: args}}}}
}

// Before interject, six sites appended independently and several could
// fire on the same step — a worker could be told to broaden its search
// and to stop searching and act, in the same breath. Adjacent injected
// user messages are the signature of that: nothing separates them
// because they came from one step.
func TestAgent_Interject_AtMostOneMessagePerStep(t *testing.T) {
	client := &stubClient{responses: []llm.ChatResponse{
		failStep("1", "a.go"),
		failStep("2", "b.go"), // trips stuck AND search-fatigue together
		failStep("3", "c.go"), // trips tool-loop AND search-fatigue together
		failStep("4", "d.go"),
		say("giving up"),
	}}
	a := New(Config{
		Client: client, Tools: NewRegistry(errStub{"flaky"}), System: "sys",
		SkipVerify: true, MaxStuckSteps: 2, MaxExploratorySteps: 2,
	})

	if _, err := a.Run(context.Background(), "task"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var prevWasInjected bool
	for i, m := range client.lastHistory {
		injected := m.Role == llm.RoleUser && i > 0
		if injected && prevWasInjected {
			t.Fatalf("two nudges injected in one step:\n%q\nthen\n%q",
				client.lastHistory[i-1].Content, m.Content)
		}
		prevWasInjected = injected
	}
}
