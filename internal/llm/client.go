// Package llm defines the wire-level contract between the agent loop and a
// chat-completion backend. Everything backend-specific (quirky JSON,
// missing fields, weak-model workarounds) lives inside a concrete Client
// implementation, never here and never in the agent loop.
package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a single function call the model wants to make.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Message is one turn in the conversation. ToolCalls is only set on
// assistant messages that want to invoke tools; ToolCallID is only set on
// tool-result messages answering a specific call.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// ToolDef is what we tell the model a tool looks like.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON schema for the arguments object
}

type ChatRequest struct {
	Messages []Message
	Tools    []ToolDef

	// MaxTokens caps how many tokens the backend may generate for this
	// response. Leaving this at the backend's own default is what causes
	// large generations (e.g. a full HTML+CSS+JS file in one write_file
	// call) to cut off mid-JSON — see Config.MaxTokens in package agent.
	MaxTokens int
}

// Usage reports real token counts from the backend's own tokenizer — not
// an approximation like tiktoken, which would be measuring the wrong
// vocabulary entirely for a non-OpenAI local model. Agent.Run uses
// PromptTokens against Config.ContextLimit to know how full the context
// actually is.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

type ChatResponse struct {
	Message Message
	Usage   Usage
}

// Client talks to exactly one backend. Implementations own everything
// specific to that backend: prompt formatting, tool-call JSON quirks,
// repairing malformed output, etc. The agent loop never knows which
// backend it's talking to.
type Client interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
