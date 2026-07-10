// Concrete Client for koboldcpp / llama.cpp servers exposing an
// OpenAI-compatible /v1/chat/completions endpoint. All backend-specific
// quirks (weak local models mangling tool-call JSON, ignoring schemas,
// Jinja template inconsistencies) are absorbed here, behind Client — the
// agent loop never has to know about any of it.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type KoboldClient struct {
	baseURL string
	model   string
	http    *http.Client

	// Debug, if set, receives the raw JSON request and response bodies
	// for every call — the fastest way to find out whether something like
	// koboldcpp's own "[TOOLCALL REASONING]" console output actually rides
	// along in the HTTP response, or only ever exists in the server's own
	// terminal.
	Debug io.Writer

	// Grammar, if set, is sent as a GBNF constraint on every response.
	// koboldcpp already grammar-constrains the arguments of a tool call it
	// decides to make; it does NOT constrain the plain-text path taken
	// when it decides not to call one — which is exactly where a model's
	// own native tool-call template tokens (e.g. "<|tool_call>...") can
	// leak into what should be a normal answer. See DefaultGrammar.
	Grammar string
}

// DefaultGrammar forbids a response from starting with '<' or '[', and
// otherwise allows anything — plain prose, Unicode text, or a real
// tool-call JSON object (which always starts with '{'). It blocks the two
// leak shapes actually observed: a model's native "<tool_call>..." tags,
// and a model writing out a whole tool-call envelope as a JSON *array* in
// plain content instead of using the structured tool_calls field.
//
// This is fundamentally reactive — it's not a general solution to "models
// sometimes leak structured output as text," just a growing blocklist of
// the specific shapes we've actually hit. If a third shape shows up,
// don't reach for a third character to ban; that's the signal to stop
// patching this grammar and instead define one strict response envelope
// of our own (via koboldcpp's JSON-schema-as-grammar support) that the
// model is constrained to use for every turn, not just tool calls.
//
// Depends on live backend behavior, not just Go: its docs say arbitrary
// extra fields (including "grammar") pass through /v1/chat/completions,
// and that grammar can coexist with "tools", but the exact interaction
// between an externally supplied grammar and koboldcpp's own internal
// tool-call grammar pass isn't something testable without a live server.
// Verify with -debug: confirm "grammar" appears in the logged request,
// and that move_file/list_files calls still work normally.
const DefaultGrammar = `root ::= first rest
first ::= [^<\[` + "\\x00" + `]
rest ::= [^` + "\\x00" + `]*
`

func NewKoboldClient(baseURL, model string) *KoboldClient {
	return &KoboldClient{
		baseURL: baseURL,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Minute},
	}
}

// --- wire format for the OpenAI-compatible endpoint ---

type wireFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Grammar     string        `json:"grammar,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message wireMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// debugSyncer lets logDebug flush *os.File-backed Debug writers
// immediately, so agent-debug.log is readable while the agent is still
// running instead of only after it exits.
type debugSyncer interface{ Sync() error }

func (c *KoboldClient) logDebug(format string, args ...any) {
	if c.Debug == nil {
		return
	}
	fmt.Fprintf(c.Debug, format, args...)
	if f, ok := c.Debug.(debugSyncer); ok {
		_ = f.Sync()
	}
}

func (c *KoboldClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// A per-request grammar (structured plan/verdict calls) wins over the
	// client-level default; both empty means no constraint at all.
	grammar := c.Grammar
	if req.Grammar != "" {
		grammar = req.Grammar
	}
	wreq := wireRequest{Model: c.model, Grammar: grammar, MaxTokens: req.MaxTokens, Temperature: req.Temperature}

	for _, m := range req.Messages {
		wm := wireMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			// Per the OpenAI tool-call wire format, function.arguments is a
			// JSON-encoded *string*, not a raw object — encodeArguments
			// re-wraps our internal object form before it goes back out.
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireFunction{
					Name:      tc.Name,
					Arguments: encodeArguments(tc.Arguments),
				},
			})
		}
		wreq.Messages = append(wreq.Messages, wm)
	}

	for _, t := range req.Tools {
		wt := wireTool{Type: "function"}
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Parameters
		wreq.Tools = append(wreq.Tools, wt)
	}

	body, err := json.Marshal(wreq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal request: %w", err)
	}
	c.logDebug("[%s] --- request ---\n%s\n\n", time.Now().Format(time.RFC3339), body)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("backend returned %s", resp.Status)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read response body: %w", err)
	}
	c.logDebug("[%s] --- response (status %s) ---\n%s\n\n", time.Now().Format(time.RFC3339), resp.Status, bodyBytes)

	var wresp wireResponse
	if err := json.Unmarshal(bodyBytes, &wresp); err != nil {
		snippet := string(bodyBytes)
		if len(snippet) > 500 {
			snippet = snippet[:500] + "...(truncated)"
		}
		return ChatResponse{}, fmt.Errorf("decode response: %w — backend said: %s", err, snippet)
	}
	if len(wresp.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("backend returned no choices")
	}

	wm := wresp.Choices[0].Message
	out := Message{
		Role:    Role(wm.Role),
		Content: wm.Content,
	}
	for _, tc := range wm.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: repairArguments(normalizeArguments(tc.Function.Arguments)),
		})
	}

	return ChatResponse{
		Message: out,
		Usage: Usage{
			PromptTokens:     wresp.Usage.PromptTokens,
			CompletionTokens: wresp.Usage.CompletionTokens,
		},
	}, nil
}

// MaxContextLength asks the backend for the actual context size it was
// loaded with (koboldcpp's /api/extra/true_max_context_length) — the real
// ceiling for the model currently running, not a guess or a hardcoded
// constant. Call once at startup and feed the result into
// agent.Config.ContextLimit. If the backend doesn't support this endpoint
// (older koboldcpp, or a different server), the caller should treat the
// error as "budget tracking unavailable" and proceed without it rather
// than failing the whole run.
func (c *KoboldClient) MaxContextLength(ctx context.Context) (int, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/extra/true_max_context_length", nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("backend returned %s", resp.Status)
	}

	var out struct {
		Value int `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	return out.Value, nil
}

// normalizeArguments handles the real shape of this wire format: per spec,
// function.arguments is a JSON-encoded *string*, e.g. the bytes
// `"{\"command\": \"ls\"}"` rather than `{"command": "ls"}` directly. Some
// local servers send the object form anyway, so accept either: if the raw
// bytes are themselves a JSON string, unwrap it once; otherwise pass
// through unchanged.
func normalizeArguments(raw json.RawMessage) json.RawMessage {
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return json.RawMessage(asString)
	}
	return raw
}

// encodeArguments is normalizeArguments' mirror image for outgoing
// requests: our internal ToolCall.Arguments is always the plain object
// form, so re-wrap it as a JSON string before it goes on the wire,
// matching what this backend actually expects.
func encodeArguments(args json.RawMessage) json.RawMessage {
	wrapped, err := json.Marshal(string(args))
	if err != nil {
		return json.RawMessage(`"{}"`)
	}
	return json.RawMessage(wrapped)
}

// repairArguments passes wire-format arguments through unchanged. Valid
// JSON (including a bare string instead of an object) reaches tools as-is
// so they can fail fast with a clear schema error. Invalid bytes also pass
// through — tools report bad arguments rather than masking mistakes behind
// an {"input":"..."} wrapper.
func repairArguments(raw json.RawMessage) json.RawMessage {
	return raw
}
