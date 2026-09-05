package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureServer(t *testing.T, captured *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{}}`))
	}))
}

// A per-request grammar (structured plan/verdict calls) must win over the
// client-level leak-blocking default — the whole point is that one call
// can demand a strict JSON schema without reconfiguring the client.
func TestChat_RequestGrammarOverridesClientGrammar(t *testing.T) {
	var captured []byte
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	c.Grammar = DefaultGrammar

	const planGrammar = `root ::= "{}"`
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "plan"}},
		Grammar:  planGrammar,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var sent struct {
		Grammar string `json:"grammar"`
	}
	if err := json.Unmarshal(captured, &sent); err != nil {
		t.Fatalf("decode sent request: %v", err)
	}
	if sent.Grammar != planGrammar {
		t.Fatalf("grammar = %q, want the per-request grammar to win", sent.Grammar)
	}
}

func TestChat_ClientGrammarStillUsedWhenRequestGrammarEmpty(t *testing.T) {
	var captured []byte
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	c.Grammar = DefaultGrammar

	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var sent struct {
		Grammar string `json:"grammar"`
	}
	if err := json.Unmarshal(captured, &sent); err != nil {
		t.Fatalf("decode sent request: %v", err)
	}
	if sent.Grammar != DefaultGrammar {
		t.Fatalf("grammar = %q, want client-level DefaultGrammar", sent.Grammar)
	}
}

func TestChat_TemperatureSentWhenSet(t *testing.T) {
	var captured []byte
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages:    []Message{{Role: RoleUser, Content: "plan"}},
		Temperature: 0.3,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var sent struct {
		Temperature float64 `json:"temperature"`
	}
	if err := json.Unmarshal(captured, &sent); err != nil {
		t.Fatalf("decode sent request: %v", err)
	}
	if sent.Temperature != 0.3 {
		t.Fatalf("temperature = %v, want 0.3", sent.Temperature)
	}
}

func TestChat_FinishReasonMapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Let me just"},` +
			`"finish_reason":"length"}],"usage":{"prompt_tokens":7426,"completion_tokens":38}}`))
	}))
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	resp, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "go"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.FinishReason != "length" {
		t.Fatalf("FinishReason = %q, want length", resp.FinishReason)
	}
}

// Sampling is the harness's to decide, not the server's. A local backend
// supplies its own default for any field omitted, so a score measured
// under an unstated temperature cannot be compared with the next one —
// the same class of silent variable as an unstated model. Zero still
// never reaches the wire as a literal 0.0: greedy sampling makes a
// grammar-constrained weak model loop on repeated tokens.
func TestChat_UnsetTemperatureBecomesAnExplicitDefault(t *testing.T) {
	var captured []byte
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !strings.Contains(string(captured), `"temperature":0.4`) {
		t.Fatalf("expected an explicit temperature, got: %s", captured)
	}
	for _, field := range []string{`"rep_pen"`, `"dry_multiplier"`} {
		if !strings.Contains(string(captured), field) {
			t.Fatalf("expected %s to be sent, got: %s", field, captured)
		}
	}
}
