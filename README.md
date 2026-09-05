# TARS

A coding agent for local LLMs, in Go.

Dependencies are kept to the standard library plus `golang.org/x/sys`, which is
needed for Windows job objects — there is no standard-library way to kill a
process tree whose parent has already exited. A third-party package is
considered only if it is cgo-free, popular, and does something worth the weight.

Aimed at small local models (12–35B) served by koboldcpp, llama.cpp or LM Studio.
Correctness comes from the harness — mechanical checks, hard repeat guards, bounded
budgets — not from trusting the model's report of its own work.

## Requirements

- Go 1.22+
- A local server with an OpenAI-compatible `/v1/chat/completions` endpoint,
  listening on `http://localhost:5001` by default.

## Usage

```bash
go run ./cmd/agent "add a Version constant to config.go"
```

Common flags:

| Flag | Meaning |
|---|---|
| `-workspace DIR` | Root the agent may read and write. Default `.` |
| `-mission` | Plan first, then run one fresh-context worker per subtask |
| `-yes` | Skip the mission plan approval gate |
| `-verify-cmd CMD` | Command run at the self-check point, e.g. `"go test ./..."` |
| `-resume PATH` | Continue an interrupted run from its saved state |
| `-debug` | Write raw request/response JSON to `agent-debug.log` |
| `-backend URL` | Backend base URL |

## Modes

**Direct** — one agent, one context, tools until done. For single-file and
question-shaped tasks.

**Mission** (`-mission`) — a grammar-constrained plan you approve, then one worker
per subtask, each starting from a compiled seed rather than the previous worker's
transcript. Every subtask is verified mechanically after it runs. Auto-enabled when
a task names two or more files.

State is written to `.agent/tasks/<id>/` after every step; `-resume` picks up there.

## Evals

`eval/probes/` holds scenarios with frozen pass criteria. Each runs in a throwaway
copy of its seed workspace and is scored three ways: what the workspace contains,
which tools were used, and what the run reported.

```bash
go run ./cmd/eval -dry                      # validate probes, no model calls
go run ./cmd/eval -runs 3                   # run everything
go run ./cmd/eval -only 0 -require-model qwen
```

Results, per-run traces and the model that produced them land in
`eval/results/<timestamp>/`. `-require-model` refuses to start unless the backend
reports the weights you expect.

`eval/prompts/` holds the human-readable write-up each probe mechanizes.

## Layout

```
cmd/agent        CLI
cmd/eval         probe runner
internal/agent   the loop: tools, repeat detection, budgets, compaction
internal/mission plan, ledger, workers, checks, replan, review
internal/roles   the four kinds of agent this project builds
internal/llm     backend client, GBNF grammar
internal/tools   file, shell, search, process and delegation tools
internal/prompts every prompt, as .txt
```

## Limitations

- Tuned for weak models. Larger tasks fail as bad plans, not bad code.
- `internal/workspace` bounds the file tools, not the process. `run_shell` executes
  arbitrary commands. Use a container or a VM if that matters.
- koboldcpp performs its own tool-call decision pass, which this harness does not
  control.
- Windows-first; the shell tools pick `cmd.exe` or `sh` by OS but see less testing
  on Linux and macOS.

## License

MIT. See [LICENSE](LICENSE).
