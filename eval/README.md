# Evals

Frozen scenarios for a weak local model, scored by a program.

- `prompts/` — the write-up for each scenario: what it stresses, what counts as
  a pass, what counts as a failure. Written for a human.
- `probes/` — the machine-checkable form of those criteria.
- `fixtures/` — the seed workspace probes start from. Copied per run, never
  written to.
- `results/` — output, gitignored.

## Running

```bash
go run ./cmd/eval -dry                          # validate probes, no model calls
go run ./cmd/eval                               # everything, once each
go run ./cmd/eval -only 0 -runs 3               # probes 03-10, three runs each
go run ./cmd/eval -require-model qwen           # refuse to start on other weights
```

Each run writes `results/<timestamp>/` containing `results.jsonl`, `meta.json`
recording which model produced the numbers, and a full request/response trace
per run.

`-runs` matters. A weak model is stochastic, and a single pass is close to no
evidence. Step counts are recorded for every run whether or not the probe
scores them — the difference between passing in 4 steps and passing in 13 is
usually the thing worth reading.

## How a probe is scored

Three independent verdicts, all of which must hold:

| Field | Asserts |
|---|---|
| `verify` | what the workspace contains afterwards, using `mission.Check` |
| `trace` | which tools were called, with `mode`, `args_regex` and `max_calls` |
| `answer` | what the run reported, by regex |

Plus `max_steps`, but only where the write-up states a limit.

A run that never reached the model is reported as `ERR` and excluded from the
rate. A run the harness had to cut off is a failure, however the workspace
happens to look.

## Adding a probe

Write the scenario in `prompts/` first, then mechanize it. Criteria come from
the write-up — a threshold invented while writing the JSON is a number that
will fail someday for no stated reason.

```json
{
  "prompt": "eval/prompts/07-fake-save.md",
  "task": "create a new file notes.txt containing the text eval-ok",
  "seed": "eval/fixtures",
  "verify": [{"type": "content_contains", "path": "notes.txt", "contains": "eval-ok"}],
  "trace": [{"tool": "write_file", "mode": "required", "why": "text in a reply saves nothing"}]
}
```

`why` is quoted in the failure, so a red row explains itself without opening
the write-up. Set `"mission": true` to run the planner pipeline instead of the
direct loop.

## Coverage

Probes exist for 03-11 and 13-17. Three write-ups are not yet mechanized:

- `01`, `02` — need a dedicated git seed fixture
- `12` — its pass condition is an OR across check kinds
