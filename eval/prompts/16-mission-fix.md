# 16 — mission fix loop

**What it stresses:** the bounded fix loop and, if it doesn't converge,
the replan path. The task deliberately includes a check a weak model's
first attempt tends to fail (a real command with a strict exit code, not
`file_exists`), so the interesting part is what happens *after* the first
`check FAILED`.

## Run

```bash
mkdir -p sandbox/mission-fix

go run ./cmd/agent -mission -workspace sandbox/mission-fix \
  "write a Python script stats.py that reads numbers.txt (one integer per line) and prints their sum, then create numbers.txt with the numbers 3, 5 and 34"
```

Steer at the approval gate: the plan should end with a `shell` check like
`python stats.py` (exit 0) — if the planner picked something vacuous the
validator should already have rejected it; if it picked `file_exists`
only, type a revision note asking for a real run as the final check.

## Judge

| Signal | Usually means |
|--------|----------------|
| `check FAILED — starting fix worker (1/2)` then `check PASSED` | the design working: real error output → focused fix |
| fix worker rewrites everything from scratch | "do not redesign" rule ignored — note it; the seed carried the failing output, check whether the model used it |
| both fix attempts fail → `replanning (1/1)` | acceptable; read whether the new plan actually changes approach or repeats it |
| replan repeats the failed approach verbatim | weak-model ceiling; the budget still bounds it — mission fails loudly after 1 replan |
| same subtask attempted more than 3 times | BUG — the attempt budget must bound this by construction |

**Pass** = mission reaches DONE with `python stats.py` printing 42, in at
most 1 + 2 fix attempts per subtask and ≤1 replan; OR mission FAILED with
a report whose facts make the failure obvious in one read.

Inspect `.agent/tasks/<id>/mission.json` afterwards: each attempt must
have its own `attempt N wrote:` / `check:` fact pair, and each fix worker
its own `workers/sN-aK.json` transcript starting from a fix seed (not a
continuation of the failed attempt's conversation).
