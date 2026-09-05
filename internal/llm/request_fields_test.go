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
	if !strings.Contains(string(captured), `"rep_pen"`) {
		t.Fatalf("expected rep_pen to be sent, got: %s", captured)
	}
	// DRY is opt-in: it penalizes verbatim repetition, and patch_file
	// requires reproducing old_content exactly.
	if strings.Contains(string(captured), `"dry_multiplier"`) {
		t.Fatalf("DRY sent without being asked for, got: %s", captured)
	}
}

func TestChat_DRYIsSentOnlyWhenEnabled(t *testing.T) {
	var captured []byte
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	c.DRY = true
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(string(captured), `"dry_multiplier"`) {
		t.Fatalf("DRY enabled but not sent, got: %s", captured)
	}
}

// The bug this pair of tests exists for: llama-server documents `grammar`
// under POST /completion only. On /v1/chat/completions it accepts the
// field, ignores it, and answers 200. Nine mission runs planned with no
// constraint at all and failed as "output is not valid JSON" before
// anyone looked at what went out on the wire.
//
// So assert the wire form per backend, not just that something was set.
func TestChat_StructuredRequestUsesTheFormEachBackendAccepts(t *testing.T) {
	const gbnf = `root ::= "{}"`
	const schema = `{"type":"object"}`

	for _, tc := range []struct {
		name                  string
		newClient             func(string) *Server
		wantGrammar, wantForm bool
	}{
		{"kobold takes GBNF", func(u string) *Server { return NewKoboldClient(u, "local") }, true, false},
		{"llama takes response_format", func(u string) *Server { return NewLlamaClient(u, "local") }, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured []byte
			srv := captureServer(t, &captured)
			defer srv.Close()

			if _, err := tc.newClient(srv.URL).Chat(context.Background(), ChatRequest{
				Messages:   []Message{{Role: RoleUser, Content: "plan"}},
				Grammar:    gbnf,
				JSONSchema: schema,
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}

			var sent struct {
				Grammar        string `json:"grammar"`
				ResponseFormat *struct {
					Type   string          `json:"type"`
					Schema json.RawMessage `json:"schema"`
				} `json:"response_format"`
			}
			if err := json.Unmarshal(captured, &sent); err != nil {
				t.Fatalf("decode sent request: %v", err)
			}

			if got := sent.Grammar != ""; got != tc.wantGrammar {
				t.Errorf("grammar sent = %v, want %v (%q)", got, tc.wantGrammar, sent.Grammar)
			}
			if got := sent.ResponseFormat != nil; got != tc.wantForm {
				t.Fatalf("response_format sent = %v, want %v", got, tc.wantForm)
			}
			if tc.wantForm && string(sent.ResponseFormat.Schema) != schema {
				t.Errorf("schema = %s, want %s", sent.ResponseFormat.Schema, schema)
			}
		})
	}
}

// Every constrained call site must supply both encodings. Supplying only
// the GBNF is silently unconstrained on llama; only the schema is
// silently unconstrained on kobold. Neither backend complains.
func TestChat_OneEncodingAloneLeavesABackendUnconstrained(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  ChatRequest
	}{
		{"GBNF only", ChatRequest{Grammar: `root ::= "{}"`}},
		{"schema only", ChatRequest{JSONSchema: `{"type":"object"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var constrained int
			for _, newClient := range []func(string) *Server{
				func(u string) *Server { return NewKoboldClient(u, "local") },
				func(u string) *Server { return NewLlamaClient(u, "local") },
			} {
				var captured []byte
				srv := captureServer(t, &captured)

				req := tc.req
				req.Messages = []Message{{Role: RoleUser, Content: "plan"}}
				if _, err := newClient(srv.URL).Chat(context.Background(), req); err != nil {
					t.Fatalf("Chat: %v", err)
				}
				srv.Close()

				if strings.Contains(string(captured), `"grammar"`) ||
					strings.Contains(string(captured), `"response_format"`) {
					constrained++
				}
			}
			if constrained == 2 {
				t.Error("both backends constrained by one encoding; " +
					"if that is now true, drop the requirement to send both")
			}
		})
	}
}
