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

	// contentGrammar constrains an ordinary, non-structured response, or
	// is empty for no constraint. It is a backend decision because the
	// same tokens mean opposite things on the two servers: koboldcpp
	// parses tool calls itself, so a model writing its native tool-call
	// tags into content is a leak worth blocking, while on llama-server
	// with --jinja those tags are the protocol.
	contentGrammar() string
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
