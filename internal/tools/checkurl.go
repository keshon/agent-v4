package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// CheckURL makes a real HTTP request instead of trusting a dev server's
// startup banner — "Local: http://localhost:5173/" printed to stdout
// doesn't mean the port is actually reachable. A failed connection is a
// normal, useful result here, not a tool error: "connection refused" is
// exactly the answer the model needs to see.
type CheckURL struct{}

func (CheckURL) Name() string { return "check_url" }
func (CheckURL) Description() string {
	return "Make an HTTP GET request to a URL and report the status code, or the connection " +
		"error if it's not reachable. Use this to verify a server is actually up instead of " +
		"trusting its startup output."
}
func (CheckURL) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"url": {"type": "string"}},
		"required": ["url"]
	}`)
}

func (CheckURL) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return "", fmt.Errorf("bad url: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Not a tool failure — this IS the diagnostic the model asked for.
		return fmt.Sprintf("request failed: %v", err), nil
	}
	defer resp.Body.Close()
	return fmt.Sprintf("status: %d %s", resp.StatusCode, resp.Status), nil
}
