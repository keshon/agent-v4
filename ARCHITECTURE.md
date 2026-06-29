# agent-v4 architecture

One loop, four packages. No "policy" layer, no "orchestrate" layer sitting
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

internal/workspace   — The only thing allowed to touch the filesystem
                       directly. Tools go through it; nothing else does.
```

## Rules for v4 (read before adding anything)

1. **One loop.** If you're tempted to add a second "planner" or
   "orchestrator" next to `agent.Agent`, don't — extend the loop itself or
   add a tool instead.
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
- **Planning ahead of execution.** The loop is reactive (think → act →
  think) on purpose. Add explicit upfront planning only if you hit a
  concrete case the reactive loop handles badly — don't pre-build it.
- **Multiple backends.** When you actually need a second one, add a second
  `internal/llm/*.go` file implementing `Client`. Don't build an abstraction
  for backends you don't have yet.

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
history out from under it. Deliberately not doing automatic mechanical
history compaction yet: deciding what's safe to drop without breaking
tool_call_id pairing is a bigger, riskier piece of design than "tell the
model the number and let it decide," which is also what was actually
asked for. Revisit if nudging alone proves insufficient.

## Parallel tool calls

Multiple tool calls within a single step run concurrently
(`sync.WaitGroup`), not in a `for` loop one at a time. This is what makes
two `delegate_task` calls in the same step actually run in parallel
instead of sequentially. Trade-off, stated plainly: a model that issues a
write then a read of the *same* file in one step can no longer rely on
them executing in that order — multi-call steps in practice are almost
always independent reads or independent delegations, so this was judged
worth it. If that assumption breaks, the fix is narrowing which `Tool`s
are eligible for concurrent execution, not reverting the whole mechanism.

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

## Watch this: state piling up in run()

`run()` now threads six pieces of mutable state through the loop body:
`stuckSteps`, `verifiedOnce`, `lastSignature`, `warnedThreshold`,
`lastPromptTokens`, `mutatingSucceeded`. Still readable as named locals
today. If a seventh cross-cutting concern gets added the same way, that's
the signal to bundle these into a small `runState` struct instead of
adding local #7 — not before, since premature structure here is exactly
the kind of layer this project keeps deciding against elsewhere.

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
