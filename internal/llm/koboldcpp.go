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

type Server struct {
	baseURL string
	model   string
	dialect dialect
	http    *http.Client

	// Debug, if set, receives the raw JSON request and response bodies
	// for every call — the fastest way to find out whether something like
	// koboldcpp's own "[TOOLCALL REASONING]" console output actually rides
	// along in the HTTP response, or only ever exists in the server's own
	// terminal.
	Debug io.Writer

	// DRY enables the DRY sampler on every request. Off by default; see
	// the sampling defaults for why.
	DRY bool

	// Grammar, if set, is sent as a GBNF constraint on every response.
	// koboldcpp already grammar-constrains the arguments of a tool call it
	// decides to make; it does NOT constrain the plain-text path taken
	// when it decides not to call one — which is exactly where a model's
	// own native tool-call template tokens (e.g. "<|tool_call>...") can
	// leak into what should be a normal answer. See DefaultGrammar.
	Grammar string
}

// DefaultGrammar forbids a response from starting with '<' or '[' —
// EXCEPT for a literal leading "<think>", which thinking models (Qwen3
// family) must be allowed to emit or their generation degrades at the
// first token. Otherwise it allows anything — plain prose, Unicode text,
// or a real tool-call JSON object (which always starts with '{'). It
// blocks the two leak shapes actually observed: a model's native
// "<tool_call>..." tags, and a model writing out a whole tool-call
// envelope as a JSON *array* in plain content instead of using the
// structured tool_calls field. Mid-message leaks (including anything
// after a think block) are caught in Go by looksLikeLeakedToolCall.
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
first ::= [^<\[` + "\\x00" + `] | "<think>"
rest ::= [^` + "\\x00" + `]*
`

// Default sampling. These are sent on every request so that nothing is
// left to whatever preset the server happened to start with: a local
// backend silently supplies its own defaults for any field omitted, and
// a score measured under unknown sampling cannot be compared to the next
// one. Values here are deliberate and measurable, not inherited.
//
// DRY is off by default, and that is a measured decision rather than a
// preference. It was switched on to stop a collapse — a run whose context
// filled with high-entropy base64 degenerated into one token repeated for
// hundreds of lines — and the next baseline showed every probe that
// patches a file getting slower and less reliable while every probe that
// does not stayed byte-identical.
//
// The mechanism fits: DRY penalizes verbatim repetition of sequences, and
// patch_file requires reproducing old_content exactly. Repetition is
// degeneration in prose and correctness in code, so a sampler that cannot
// tell them apart should not be on by default in a coding agent.
// KoboldClient.DRY re-enables it for anyone who wants to re-test.
const (
	defaultTemperature = 0.4
	defaultRepPen      = 1.05
	defaultRepPenRange = 1024
	defaultDRYMult     = 0.8
	defaultDRYBase     = 1.75
	defaultDRYAllowed  = 2
)

// NewKoboldClient talks to a koboldcpp server.
func NewKoboldClient(baseURL, model string) *Server {
	return newServer(baseURL, model, koboldDialect{})
}

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

func newServer(baseURL, model string, d dialect) *Server {
	return &Server{
		baseURL: baseURL,
		model:   model,
		dialect: d,
		// Long: a full context on a local model can take minutes to
		// process before a single token is generated.
		http: &http.Client{Timeout: 120 * time.Minute},
	}
}

// Backend names the server this client talks to, for recorded results.
func (c *Server) Backend() string { return c.dialect.name() }

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

	// Repetition controls. Both backends pass unknown fields through to
	// the sampler, the same route the grammar field takes, and each
	// dialect fills only its own spelling — koboldcpp's rep_pen or
	// llama-server's repeat_penalty. The DRY names happen to agree.
	RepPen        float64 `json:"rep_pen,omitempty"`
	RepPenRange   int     `json:"rep_pen_range,omitempty"`
	RepeatPenalty float64 `json:"repeat_penalty,omitempty"`
	RepeatLastN   int     `json:"repeat_last_n,omitempty"`
	DRYMult       float64 `json:"dry_multiplier,omitempty"`
	DRYBase       float64 `json:"dry_base,omitempty"`
	DRYAllowed    int     `json:"dry_allowed_length,omitempty"`
}

type wireResponse struct {
	Choices []struct {
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
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

func (c *Server) logDebug(format string, args ...any) {
	if c.Debug == nil {
		return
	}
	fmt.Fprintf(c.Debug, format, args...)
	if f, ok := c.Debug.(debugSyncer); ok {
		_ = f.Sync()
	}
}

func (c *Server) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// A per-request grammar (structured plan/verdict calls) wins over the
	// client-level default; both empty means no constraint at all.
	grammar := c.Grammar
	if req.Grammar != "" {
		grammar = req.Grammar
	}
	temperature := req.Temperature
	if temperature <= 0 {
		temperature = defaultTemperature
	}
	wreq := wireRequest{
		Model:       c.model,
		Grammar:     grammar,
		MaxTokens:   req.MaxTokens,
		Temperature: temperature,
	}
	c.dialect.applySampling(&wreq, c.DRY)

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
		Message:      out,
		FinishReason: wresp.Choices[0].FinishReason,
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
// MaxContextLength reports the context window the server was actually
// started with, so budget tracking uses a fact rather than a guess.
func (c *Server) MaxContextLength(ctx context.Context) (int, error) {
	return c.dialect.contextLimit(ctx, c.http, c.baseURL)
}

// ModelName asks the backend which model is actually loaded, which is
// rarely what the -model flag says: local servers usually ignore it and
// serve whatever weights they were started with. Worth recording next to
// any measurement — a pass rate compared against one from a different
// model, or the same model at a different quantization, is worse than no
// number at all.
func (c *Server) ModelName(ctx context.Context) (string, error) {
	return c.dialect.modelName(ctx, c.http, c.baseURL)
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
