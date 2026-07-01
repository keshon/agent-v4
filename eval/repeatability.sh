#!/usr/bin/env bash
# A/B matrix helper for separating repeatable vs accidental eval success.
# Does not automate pass/fail — records logs for human comparison.
#
# Usage (from repo root, koboldcpp up):
#   ./eval/repeatability.sh 01-git-ambiguous 3
#
# Runs prompt 01 three times with:
#   A) default agent (system git line + skills on disk)
#   B) same, after ./eval/reset-fixtures.sh between runs if needed
#
# For git-specific isolation, temporarily rename skills/git:
#   mv skills/git skills/git.off && ./eval/repeatability.sh ... ; mv skills/git.off skills/git
set -euo pipefail

if [[ $# -lt 2 ]]; then
	echo "usage: $0 <prompt-id-or-file> <runs>" >&2
	echo "example: $0 01-git-ambiguous 5" >&2
	exit 1
fi

PROMPT_ID="$1"
RUNS="$2"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

PROMPT_FILE=""
if [[ -f "eval/prompts/${PROMPT_ID}.md" ]]; then
	PROMPT_FILE="eval/prompts/${PROMPT_ID}.md"
elif [[ -f "eval/prompts/${PROMPT_ID}" ]]; then
	PROMPT_FILE="eval/prompts/${PROMPT_ID}"
else
	echo "prompt not found: $PROMPT_ID" >&2
	exit 1
fi

# Extract quoted prompt from markdown (first backtick block in Run section).
PROMPT_TEXT="$(awk '/^```bash$/{flag=1;next}/^```$/{if(flag){exit}}flag' "$PROMPT_FILE" | grep 'go run' | sed -n 's/.*"\(.*\)".*/\1/p' | head -1)"
if [[ -z "$PROMPT_TEXT" ]]; then
	echo "could not extract prompt text from $PROMPT_FILE" >&2
	exit 1
fi

OUT_DIR="${ROOT}/eval/log/repeatability"
mkdir -p "$OUT_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"

echo "prompt: $PROMPT_TEXT"
echo "runs: $RUNS -> $OUT_DIR/${PROMPT_ID}-${STAMP}-*.log"

for i in $(seq 1 "$RUNS"); do
	LOG="${OUT_DIR}/${PROMPT_ID}-${STAMP}-run${i}.log"
	echo "=== run $i/$RUNS ===" | tee "$LOG"
	go run ./cmd/agent -log-max 300 -workspace eval/fixtures "$PROMPT_TEXT" 2>&1 | tee -a "$LOG"
	echo "" | tee -a "$LOG"
done

echo "done. compare step counts and tool choices across runs."
