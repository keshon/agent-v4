#!/usr/bin/env bash
# Reset eval/fixtures to the committed baseline and remove artifacts from
# prior manual eval runs. Run from repo root:
#   ./eval/reset-fixtures.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FIX="${ROOT}/eval/fixtures"

cd "$ROOT"

git checkout -- eval/fixtures/dup.txt eval/fixtures/hello.txt eval/fixtures/sample.go

rm -f \
	"${FIX}/goodbye.txt" \
	"${FIX}/alpha.txt" \
	"${FIX}/beta.txt" \
	"${FIX}/gamma.txt" \
	"${FIX}/notes.txt" \
	"${FIX}/README.md"

echo "fixtures reset to git baseline (dup.txt, hello.txt, sample.go)"
echo "optional: head -c 150000 /dev/urandom | base64 > eval/fixtures/big.txt"
