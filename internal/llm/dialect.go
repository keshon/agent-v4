package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// A dialect is the small part of a local model server that is not
// OpenAI-compatible.
//
// koboldcpp and llama-server both serve /v1/chat/completions and both
// accept extra sampler fields alongside it, so almost everything is
// shared. What differs is where you ask what model is loaded and how
// large its context is, and how each spells its repetition penalty. That
// is the whole of it, which is why this interface is three methods rather
// than a second client.
type dialect interface {
	// name identifies the backend in errors and recorded results.
	name() string

	// contextLimit reports the window the server was started with, and
	// modelName the weights it actually loaded. Separate because they are
	// asked separately: a caller that only wants the window should not
	// need an unrelated endpoint to exist.
	contextLimit(ctx context.Context, hc *http.Client, baseURL string) (int, error)
	modelName(ctx context.Context, hc *http.Client, baseURL string) (string, error)

	// applySampling writes this backend's spelling of the sampler
	// settings into an outgoing request.
	applySampling(req *wireRequest, dry bool)
}

// --- koboldcpp ---

type koboldDialect struct{}

func (koboldDialect) name() string { return "koboldcpp" }

func (koboldDialect) applySampling(req *wireRequest, dry bool) {
	req.RepPen = defaultRepPen
	req.RepPenRange = defaultRepPenRange
	if dry {
		req.DRYMult = defaultDRYMult
		req.DRYBase = defaultDRYBase
		req.DRYAllowed = defaultDRYAllowed
	}
}

func (koboldDialect) contextLimit(ctx context.Context, hc *http.Client, baseURL string) (int, error) {
	var out struct {
		Value int `json:"value"`
	}
	err := getJSON(ctx, hc, baseURL+"/api/extra/true_max_context_length", &out)
	return out.Value, err
}

func (koboldDialect) modelName(ctx context.Context, hc *http.Client, baseURL string) (string, error) {
	var out struct {
		Result string `json:"result"`
	}
	err := getJSON(ctx, hc, baseURL+"/api/v1/model", &out)
	return out.Result, err
}

// --- llama-server ---

type llamaDialect struct{}

func (llamaDialect) name() string { return "llama-server" }

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

func getJSON(ctx context.Context, hc *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
