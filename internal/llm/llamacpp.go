// llama.cpp llama-server specific behaviour.
//
// Worth running with --jinja: llama-server then applies the model's own
// chat template and its per-model tool-call format, and constrains tool
// arguments with a grammar built from the tool's JSON schema. That is the
// same job DefaultGrammar does crudely on koboldcpp, done properly and
// per model.
package llm

import (
	"context"
	"net/http"
)

// NewLlamaClient talks to a llama.cpp llama-server.
//
// Worth running with --jinja: llama-server then applies the model's own
// chat template and its per-model tool-call format, and constrains tool
// arguments with a grammar derived from the tool's JSON schema. That is
// the same job the DefaultGrammar blocklist here does badly, done
// properly and per model.
func NewLlamaClient(baseURL, model string) *Server {
	return newServer(baseURL, model, llamaDialect{})
}

type llamaDialect struct{}

func (llamaDialect) name() string { return "llama-server" }

// contentGrammar is empty on purpose. llama-server builds its own lazy
// grammar from the tools it was given and triggers it on the model's
// tool-call opener, so an outside constraint can only fight it — and
// DefaultGrammar would fight it badly. Gemma 4 opens a tool call with
// "<|tool_call>" and its reasoning with "<|channel>thought", both of
// which DefaultGrammar forbids at the first token.
func (llamaDialect) contentGrammar() string { return "" }

// llama-server spells the repetition penalty the way llama.cpp's sampler
// does; the DRY fields happen to match koboldcpp's.
func (llamaDialect) applySampling(req *wireRequest, dry bool) {
	req.RepeatPenalty = defaultRepPen
	req.RepeatLastN = defaultRepPenRange
	if dry {
		req.DRYMult = defaultDRYMult
		req.DRYBase = defaultDRYBase
		req.DRYAllowed = defaultDRYAllowed
	}
}

// /props carries both the loaded model's path and the context size the
// server was actually started with.
type llamaProps struct {
	ModelPath string `json:"model_path"`
	Default   struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

func (llamaDialect) contextLimit(ctx context.Context, hc *http.Client, baseURL string) (int, error) {
	var p llamaProps
	err := getJSON(ctx, hc, baseURL+"/props", &p)
	return p.Default.NCtx, err
}

func (llamaDialect) modelName(ctx context.Context, hc *http.Client, baseURL string) (string, error) {
	var p llamaProps
	err := getJSON(ctx, hc, baseURL+"/props", &p)
	return p.ModelPath, err
}
