package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// llama-server reports both facts from one endpoint; koboldcpp from two.
// The client asks for them separately, so neither backend may require the
// other's endpoint to exist.
func TestLlamaDialect_ReadsModelAndContextFromProps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/props" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model_path":                  "/models/qwen3.gguf",
			"default_generation_settings": map[string]any{"n_ctx": 32768},
		})
	}))
	defer srv.Close()

	c := NewLlamaClient(srv.URL, "local")
	if c.Backend() != "llama-server" {
		t.Errorf("Backend() = %q", c.Backend())
	}
	name, err := c.ModelName(context.Background())
	if err != nil || name != "/models/qwen3.gguf" {
		t.Fatalf("ModelName = %q, %v", name, err)
	}
	n, err := c.MaxContextLength(context.Background())
	if err != nil || n != 32768 {
		t.Fatalf("MaxContextLength = %d, %v", n, err)
	}
}

// Each backend spells the repetition penalty its own way, and sending the
// other's spelling would be silently ignored — the sampler would run
// unpenalised while the request looked correct.
func TestDialects_UseTheirOwnSamplerSpelling(t *testing.T) {
	for _, tc := range []struct {
		name       string
		newClient  func(string, string) *Server
		want, deny string
	}{
		{"kobold", NewKoboldClient, `"rep_pen"`, `"repeat_penalty"`},
		{"llama", NewLlamaClient, `"repeat_penalty"`, `"rep_pen"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured []byte
			srv := captureServer(t, &captured)
			defer srv.Close()

			c := tc.newClient(srv.URL, "local")
			if _, err := c.Chat(context.Background(), ChatRequest{
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if !strings.Contains(string(captured), tc.want) {
				t.Errorf("missing %s in %s", tc.want, captured)
			}
			if strings.Contains(string(captured), tc.deny) {
				t.Errorf("sent the other backend's %s in %s", tc.deny, captured)
			}
		})
	}
}

// DefaultGrammar blocks a response that starts with '<'. On koboldcpp
// that stops a model writing its native tool-call tags into content. On
// llama-server those tags are the protocol — gemma4 opens a tool call
// with "<|tool_call>" and its reasoning with "<|channel>thought" — so
// sending the same grammar there forbids the model's own tool calls at
// the first token.
func TestDialects_ContentGrammarIsBackendSpecific(t *testing.T) {
	for _, tc := range []struct {
		name      string
		newClient func(string, string) *Server
		wantGuard bool
	}{
		{"kobold", NewKoboldClient, true},
		{"llama", NewLlamaClient, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured []byte
			srv := captureServer(t, &captured)
			defer srv.Close()

			c := tc.newClient(srv.URL, "local")
			if _, err := c.Chat(context.Background(), ChatRequest{
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			sent := strings.Contains(string(captured), `"grammar"`)
			if sent != tc.wantGuard {
				t.Errorf("content grammar sent = %v, want %v: %s", sent, tc.wantGuard, captured)
			}
		})
	}
}

// A structured call carries its own schema grammar, which is correct on
// either backend and must not be replaced by the content guard.
func TestServer_RequestGrammarWinsOverTheBackendDefault(t *testing.T) {
	var captured []byte
	srv := captureServer(t, &captured)
	defer srv.Close()

	c := NewKoboldClient(srv.URL, "local")
	if _, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Grammar:  `root ::= "yes" | "no"`,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(string(captured), `yes`) {
		t.Errorf("per-request grammar was overridden: %s", captured)
	}
}
