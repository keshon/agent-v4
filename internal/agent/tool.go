// Package agent contains the model-agnostic, backend-agnostic agent loop:
// talk to an llm.Client, run whatever tools it asks for, and decide when to
// stop or change strategy. It has no idea koboldcpp or filesystem tools
// exist — those live in internal/llm and internal/tools.
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-v4/internal/llm"
)

// Tool is anything the agent can call by name with JSON arguments.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON schema for the arguments object
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry is a fixed set of tools, looked up by name.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		r.tools[t.Name()] = t
	}
	return r
}

// Defs returns the tool definitions to hand to the LLM client.
func (r *Registry) Defs() []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	return defs
}

func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return t.Run(ctx, args)
}
