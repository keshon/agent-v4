# agent-v4 architecture

One loop, five packages. No "policy" layer, no "orchestrate" layer sitting
next to the agent doing the same job twice.

```
internal/llm        — Client interface + one backend implementation.
                       All backend quirks (weak-model JSON, prompt format)
                       live here and nowhere else.

internal/agent       — Tool interface, Registry, and the Agent loop itself.
                       Doesn't know what a tool *does*, or which backend
                       it's talking to.

internal/tools       — Concrete tools: filesystem, shell, delegation.
                       Delegation is just a tool that builds and runs
                       another Agent — not a separate architecture.

internal/mission     — The -mission harness: plan → execute → verify as a
                       deterministic state machine over the same Agent
                       loop. See "Mission mode" below for why this is the
                       documented exception to rule #1, not a violation.

internal/workspace   — The only thing allowed to touch the filesystem
                       directly. Tools go through it; nothing else does.
```

## Rules for v4 (read before adding anything)

1. **One loop.** If you're tempted to add a second "planner" or
   "orchestrator" next to `agent.Agent`, don't — extend the loop itself or
   add a tool instead. *The documented exception is `internal/mission`
   (see "Mission mode" below): it hit exactly the concrete case this rule
   reserved — a complex multi-file task the reactive loop handles badly —
   and it orchestrates by reusing the loop (`agent.New` per worker), never
   by duplicating its decisions.*
2. **Backend quirks stay in `internal/llm`.** If a local model does
   something weird (malformed tool-call JSON, ignored schema, whatever),
   fix it in the Client implementation, not in the loop.
3. **Delegation is a tool, not a subsystem.** A subagent is just
   `agent.New(...)` called again, usually with a smaller tool set so it
   can't recurse forever.
4. **Nothing touches the filesystem except through `workspace.Workspace`.**
5. **`.agent/`, `sandbox/`, `temp/`** (agent-generated working files) never
   go in the repo. They're workspace output, not source — see
   `.gitignore`.

## What's deliberately not here yet

- **Persistence / run history.** Add a small `internal/observe` package
  later if you actually need to replay runs — not before you hit that need.
- **Multiple backends.** When you actually need a second one, add a second
  `internal/llm/*.go` file implementing `Client`. Don't build an abstraction
  for backends you don't have yet.

## Mission mode — why upfront planning finally got built

The original rule here was "the loop is reactive on purpose; add explicit
upfront planning only if you hit a concrete case the reactive loop handles
badly — don't pre-build it." The concrete case arrived: any genuinely
complex multi-file task ("build a web Doom-style FPS"). The failure isn't
one bug — it's structural. Every defensive mechanism below (leak
detection, stuck detection, search fatigue, compaction, the verify gate)
patches the same root cause: **a weak model asked to be its own executive
over an ever-growing append-only transcript.** By step 40 the original
goal is buried under tool output, free context is near zero, and no nudge
can restore coherence. That's not a reasoning bug to patch — it's a
capability ceiling to design around.

`internal/mission` moves the executive function into the harness:

- **The ledger is the source of truth, not the conversation.** A
  `Mission` (task verbatim, flat subtask list with acceptance criteria and
  typed checks) lives in `.agent/tasks/<id>/mission.json`, saved atomically
  after every state change. Workers see a rendering of it; no worker ever
  sees another worker's transcript.
- **Phases are decided in Go, not by the model.** explore → plan →
  execute → verify, a `switch` in `Runner.Run`. The model fills in
  content (map notes, a plan, a subtask's work); the harness decides what
  happens next. Explore is mostly mechanical (`mapgen.go` walks the tree,
  reads manifest heads) with an optional read-only annotation worker
  whose failure never blocks anything.
- **Plan generation is decision-narrowed.** A tool-free `Chat` call with a
  per-request GBNF grammar (`mission.PlanGrammar`) — the model *cannot*
  emit anything but schema-shaped JSON. Grammar handles syntax; a Go
  validator handles structure (no echo-checks, no hallucinated existing
  paths); the human approval gate handles semantics. Three cheap layers,
  each catching what the previous can't. Grammar-constrained calls carry
  no tools, so the koboldcpp grammar×tools interaction question (see
  "Grammar as a lever") never arises.
- **Each subtask runs in a fresh worker** — `agent.New(...)`, the same
  loop with all its detectors, exactly like `delegate_task` spawns
  subagents. The seed is compiled from the ledger (`mission.CompileSeed`):
  task verbatim, plan status, this subtask's goal/acceptance, file hints ∪
  mission-wide mutated paths. No file contents inlined — workers have read
  tools; context is a weak model's scarcest resource.
- **Completed work is recorded as measured fact.** Which files a worker
  actually wrote comes from `agent.RunReport` (parsed from successful
  mutating calls), never from the model's claims; whether the work is done
  comes from the subtask's declared check (`file_exists`/`shell`/`http`),
  run mechanically by the harness. This is `VerifyZeroWrites` doctrine
  promoted from a nudge to the data model.
- **Failure gets bounded correction, then loud reporting.** A failed
  check gets up to `MaxFixAttempts` (default 2) fresh fix workers, each
  seeded with the check's *actual output* and the files previous attempts
  really touched. Exhausted, the one remaining lever is `MaxReplans`
  (default 1) replans: a grammar-constrained call plans the *remaining*
  work only — executed subtasks are frozen with their history, the failed
  one keeps its facts (replacement, not erasure). Total worker runs are
  bounded by construction: subtasks × (1+fixes) × (1+replans). Past all
  budgets, FAILED-with-facts beats fake success.
- **A final review net, narrowed to a two-way verdict.** After all checks
  pass, a read-only worker inspects the result for whole-task gaps
  (cross-file references, uncovered parts of the task), and its free-text
  report is reduced to `{"verdict":"ok"|"gaps"}` by the decision grammar.
  Gaps spend remaining replan budget; a mission whose checks all passed
  never *fails* because of the review alone — unresolved gaps are recorded
  in the report instead.

What was deliberately **not** built: rolling summarization (a weak model
summarizing its own history is where acceptance criteria silently die —
fresh workers make it unnecessary; the one-shot compaction stays as the
in-worker safety net), model-written outcome summaries (mechanical facts
instead), nested milestone→subtask plans (two-level JSON is too hard;
`milestone` is a flat label), and `delegate_task` inside workers (the
mission runner *is* the decomposition — no recursion).

Direct mode (`agent "task"`) is unchanged and remains the default; mission
mode is opt-in via `-mission` and worth it only for tasks big enough to
amortize the planning call.

## Two kinds of bug — don't treat them the same way

Every real failure so far falls into one of two buckets, and they need
opposite responses:

- **Plumbing bugs** — our code is objectively wrong (string-encoded
  arguments parsed as an object, `sh -c` used on a Windows host). These are
  deterministic, finite, and worth fixing the moment you find one. Cover
  the fix with a test so it stays fixed.
- **Reasoning bugs** — the model itself does something dumb (wrong `ren`
  syntax, "completing" a rename by writing an empty file with the right
  name). There's no finite list of these to patch through — a weak local
  model will always find a new way to be confidently wrong. Chasing each
  one individually is how you end up back at agent-v3's pile of special
  cases.

For reasoning bugs, reach for systemic levers instead of one-off patches:

1. **Remove the failure surface.** If a model keeps botching a multi-step
   composition (read + write + delete to fake a rename), give it one
   atomic tool that can't be done halfway (`move_file`, backed by
   `os.Rename`). It can still misuse the tool, but it can't silently lose
   data while using it correctly.
2. **Don't trust "no more tool calls" as "done."** `Agent.Run` makes one
   extra round-trip before returning, asking the model to check its own
   work against the task and fix anything that's actually wrong or a
   placeholder (`Config.SkipVerify`, on by default). Not a guarantee — the
   same weak model does the checking — but a generic net instead of a
   per-scenario patch.

When a new failure shows up, ask "plumbing or reasoning" before deciding
how to fix it. Plumbing → fix and test it directly. Reasoning → ask
whether a better tool or a structural check removes the *class* of
mistake, not just this one instance.

## Grammar as a lever — and where it stops being "our code"

`KoboldClient.Grammar` (`DefaultGrammar`) is a different kind of fix from
everything above it: every previous fix in this file was verifiable in Go
alone, with a test that doesn't touch a real backend. Grammar enforcement
depends on how koboldcpp actually merges an externally supplied `grammar`
field with its own internal tool-selection grammar pass — that's not
something a Go unit test can confirm, only a live run with `-debug` can.

What we know from koboldcpp's own docs/changelog: `/v1/chat/completions`
passes through arbitrary extra fields (confirmed: `min_p`, `dry_multiplier`
work this way), `grammar` accepts a GBNF string, and koboldcpp >=1.92
already grammar-constrains the arguments of whichever tool it decides to
call. The gap `DefaultGrammar` targets is specifically the *other* path —
when it decides not to call a tool, that generation is unconstrained, and
that's where a model's own native tool-call template tokens (e.g.
`<tool_call>...`) can leak into what should be a plain answer.

If `-debug` ever shows tool calls behaving differently with the grammar on
vs off, that's the signal to stop trusting the docs and either narrow the
grammar further or fall back to `-grammar=false` for that backend.

## Why this maps onto what v3 already had

- `retry_nudge.go` → folded directly into `agent.Agent.Run`'s `stuckSteps`
  counter. The "glotat' tupnyaki" idea was right, it just needed to live in
  one place instead of its own file in a separate layer.
- `kobold_parse.go` / `repair_tools.go` → `repairArguments` in
  `internal/llm/koboldcpp.go`. Backend mess stays behind the `Client`
  interface where it can't leak into the loop.
- `orchestrate/*` (8 files) → `internal/tools/delegate.go` (1 file).
- v3's `internal/verify` → the single self-check round in `Agent.Run`,
  gated by `Config.SkipVerify`. Same intent (don't blindly trust
  completion), much smaller surface.

## Context budget — accurate, not approximated

`Config.ContextLimit` + the `Usage` returned with every `Chat` call give
the loop real awareness of how full the context is — *real* in the sense
that `Usage.PromptTokens` comes from koboldcpp's own tokenizer for
whatever model is actually loaded. A generic library like tiktoken-go
counts OpenAI's BPE vocabulary; for a local Gemma/Qwen/Llama model that's
the wrong tokenizer entirely and would silently under- or over-count.
`KoboldClient.MaxContextLength` asks the backend for its real ceiling
(`/api/extra/true_max_context_length`) instead of hardcoding one.

Crossing 75%/90% of that ceiling injects a one-time nudge telling the
model its actual budget and suggesting it wrap up or delegate remaining
work — informing the model via the prompt, not silently truncating
history out from under it. At 90%, the loop also runs a one-time
mechanical compaction (`compactHistory` in `internal/agent/compact.go`):
it drops older assistant-led step groups while keeping the system prompt,
original user task, and the last `Config.CompactKeepSteps` groups (default
8). Each dropped group is a whole step — assistant message plus every
tool result and nudge that followed — so tool_call_id pairing is never
broken. Set `CompactKeepSteps` to `-1` to disable compaction.

## Tool scheduling is mode-based, not flat-parallel

Every `Tool` declares a `Mode()`: `Concurrent` or `Exclusive`. Within one
step, `Concurrent` calls (reads, `grep_files`, `check_url`,
`delegate_task`, the background-process tools) run together via
`sync.WaitGroup`; `Exclusive` calls (`write_file`, `patch_file`,
`patch_lines`, `move_file`, `run_shell`) run one at a time, after the
Concurrent batch, never overlapping each other or it.

This replaced an earlier version where every call in a step ran in a flat
`sync.WaitGroup` regardless of what it did — which meant a model issuing
`write_file` then `read_file` on the same path in one step had no
guaranteed ordering between them. Classifying by `Mode()` removes that
failure surface without losing the actual win (independent
`delegate_task` calls still run in parallel; reads still don't block on
each other) — same "remove the failure surface" reasoning as `move_file`
itself, applied one level up.

`RunShell` is `Exclusive` unconditionally: an arbitrary shell command
can't be analyzed for what it touches, so the safe default is to never
let it overlap with anything. Background-process tools
(`start_background`/`check_background`/`stop_background`) are
`Concurrent` — they manage OS processes by id through a mutex-protected
registry (`BackgroundProcesses`), not the workspace filesystem, so two
processes starting in the same step (e.g. a backend + frontend dev
server) is safe and worth parallelizing.

## Role is layered, not swapped

`delegate_task`'s optional `role` field gets appended to the subagent's
*existing* system prompt, never replaces it. The alternative — letting the
caller hand the subagent a whole new system prompt — would mean every
delegated task quietly loses the baseline rules we've spent this whole
project hardening (move_file discipline, list_files-before-acting, etc).
A role is a voice or specialization for *this task*, not a license to forget how to
use the tools correctly.

## When does the agent delegate on its own, unprompted?

Realistically: rarely, without help. A weak local model doesn't
spontaneously recognize "this decomposes into N independent pieces, I
should parallelize" — that's exactly the kind of executive-function move
that's hard for it. Two concrete levers exist instead of hoping for
emergent judgment:

1. The context-budget nudge (75%/90%) explicitly suggests delegating when
   it's running low on room — pressure-triggered, not judgment-triggered.
2. The system prompt now states the heuristic plainly ("when a task splits
   into independent pieces, issue one delegate_task per piece in the same
   step") as a standing rule rather than something the model has to infer.

Don't expect (2) to work reliably on a weak model without it being spelled
out per-task like in the role test — if it doesn't generalize in
practice, that's a reasoning-bug, and the lesson from this project's own
framework applies: don't chase it with more code, treat it as a known
capability ceiling and keep spelling it out explicitly when it matters.

## Generation budget is dynamic, not static

`Config.MaxTokens` is a *ceiling*, not a fixed request value. `effectiveMaxTokens`
caps it against `ContextLimit - lastPromptTokens - margin` before every
call. Without this, requesting `max_tokens` close to or equal to the full
context size causes koboldcpp to warn and evict the prompt mid-generation
— it happened in practice (`max_tokens: 32768` on a 32768-token context).
The real strategic fix is still on the human side: `MaxTokens` should
stay modest (4-8k) regardless of context size — context is for *history
across steps*, not for one giant single-shot generation. The dynamic cap
is a safety net under that strategy, not a substitute for it.

## Don't trust "no tool calls" as automatically meaningful

The "model stopped calling tools" branch now has three outcomes, checked
in this order:

1. **Looks like a leaked pseudo tool-call** (`<tool_call...`, `<|tool_call...`
   anywhere in the content, not just at the start) → corrective nudge,
   loop again. A start-of-message-only grammar constraint missed this once
   it showed up mid-message — the fix moved to Go, where it's testable
   without a live backend.
2. **Genuine finish, not yet verified** → the self-check round, now
   enriched with two pieces of *measured* fact instead of asking the model
   to grade itself blind: `MutatingTools` count (0 real writes this run
   means nothing was actually saved, full stop) and `Config.Verify` output
   (real build/test command result, if one's configured).
3. **Genuine finish, already verified** → return.

## Skills — convention, not infrastructure

`skills/<name>/SKILL.md`, discovered via `list_files`+`read_file` — no
dedicated tool. Deliberately not building a "skill loader" or auto-inject
mechanism: the agent already has the tools needed to discover and read
them, and the system prompt says when to check. Adding infrastructure for
something existing tools already cover would be exactly the kind of
unneeded layer this project keeps avoiding elsewhere.

## grep_files — Go-native, not shelled out

Same reasoning as `move_file` vs `ren`/`mv`: shelling out to `findstr` vs
`grep` reintroduces the cmd.exe/sh split we already had to fix once.
`grep_files` is plain `regexp` + `filepath.Walk`, identical behavior on
any host OS, capped at 200 matches so one search can't blow the context
budget by itself.

## Watch this: state in run()

`run()` threads mutable state through a `runState` struct (`stuckSteps`,
`verifiedOnce`, `compactedOnce`, `blockFinishDueToVerify`, etc.). If a
new cross-cutting concern doesn't fit there cleanly, that's the signal to
split compaction or verify gating into a dedicated helper file — not to
add local #9 beside the struct.

## Long-running processes need a different tool, not a bigger timeout

`run_shell` waits for the command to exit. That's correct for `go build`,
wrong for `npm run dev` — a server is supposed to keep running, so
run_shell will always time out and kill it before anything useful can
happen. No prompt fixes "the only tool blocks until exit"; this needed an
actual new capability: `start_background` (non-blocking) +
`check_background` (read status/output later) + `stop_background` +
`check_url` (a real Go `net/http` request — not curl/Invoke-WebRequest,
same cross-platform reasoning as `move_file` vs `ren`/`mv`).

**Real bug found building this, not hypothetical:** `sh -c "npm run dev"`
forks `npm`/`node` as a child instead of exec-replacing itself. Killing
just the wrapper PID (`cmd.Process.Kill()`) leaves the real server process
orphaned and still holding the port — and worse, that orphan keeps the
output pipe open, which makes `cmd.Wait()` hang forever waiting for an EOF
that will never come. Fixed with OS-specific process-group kill
(`procgroup_unix.go`: `Setpgid` + `syscall.Kill(-pid, ...)`;
`procgroup_windows.go`: `taskkill /T /F`) — `stop_background` kills the
whole subtree, not just the immediate child. Covered by a regression test
that reproduces the fork-not-exec shape specifically, not just "kill a
plain sleep."

The `dev-server` skill carries the workflow these tools enable (start →
wait → check_url → diagnose from check_background's real output if it
fails) — the tools alone don't teach the sequence, same reasoning as
every other skill file here.

## Prompts live in internal/prompts/text/*.txt, not Go strings

Every piece of text sent to the model that isn't task/tool data — system
prompt, verify checkpoint, leak/stuck nudges, budget warnings, the resume
note — is a plain `.txt` file under `internal/prompts/text/`, embedded via
`go:embed` and exposed as trimmed string constants. The reason is git
diffs, not aesthetics: a one-sentence wording change now shows as a
one-line diff in a `.txt` file instead of being buried inside a multi-line
Go string-concatenation expression. `cmd/agent/main.go` and
`internal/agent/agent.go` both import `internal/prompts` rather than
defining their own inline strings — if you're about to write a new
`"..."` literal that gets sent to the model, it probably belongs there
instead.

## grep_files skips binary content — a real corruption, not a hypothetical

`grep_files` originally had no content-based binary detection — only an
`os.Open` failure (permissions) was treated as "skip." In practice, a
wildcard pattern (`.*`) matched across a long stretch of a compiled
`.exe` with no newlines, and raw non-UTF8 bytes — including control
characters — went straight into the conversation history as a "match."
This isn't just wasted context: corrupted history is exactly the kind of
thing that can derail every response after it (and very likely did,
based on a run that produced garbled mojibake output a couple of steps
later).

Fixed with a null-byte sniff on the first 8KB of each file (same
heuristic git itself uses to classify files as binary) — binary files are
skipped entirely before scanning, not just on open failure. Also added a
per-line length cap (500 chars, with a truncation marker) as a second,
independent safety net: even a legitimate text file with a pathologically
long line (minified JS) shouldn't be able to dump an unbounded amount of
content into one match.

## Search fatigue: a step can "progress" while still going nowhere

`MaxStuckSteps` catches two things: every tool call erroring, or an exact
repeat of the previous step's call. Neither catches a model that keeps
issuing genuinely *different* `grep_files`/`list_files` calls — new
patterns, new directories — none of which error, none of which repeat,
but none of which converge on an answer either. That's a different
failure shape from "stuck," and it showed up in practice: 13 steps of
narrowing regex variations and re-listing the same directories before the
binary-file corruption above derailed the run entirely.

`Config.MaxExploratorySteps` (default 8) tracks consecutive steps where
no `MutatingTools` call actually succeeded — reset to zero the moment one
does. Crossing the threshold injects one nudge (`prompts.SearchFatigue`,
fire-once like the verify checkpoint): try a genuinely different angle
(drop filters, simpler keywords, file types not yet tried), or stop and
report findings instead of searching indefinitely. This is a generic
lever, not a "grep harder" patch — it doesn't know or care what the
exploratory tool calls were, only that mutation isn't happening.
