#!/usr/bin/env bash
# Manual operator invocation only. No service control, model pull or automatic rerun.
set -Eeuo pipefail
export PATH="/snap/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"
export CGO_ENABLED=0
umask 077
cd "$(dirname "${BASH_SOURCE[0]}")/.."
work=$(mktemp -d "$HOME/qwen35-mode-comparison.XXXXXX")
finish() {
  code=$?
  printf '\nReport: %s/comparison.jsonl\nReport directory: %s\nExit code: %s\n' "$work" "$work" "$code"
}
trap finish EXIT
git rev-parse HEAD > "$work/commit.txt"
export OLLAMA_BASE_URL=http://127.0.0.1:11434
export OLLAMA_MODEL=qwen3.5:9b
export BRIEFING_BUDGET_TIMEOUT=60s
export BRIEFING_BUDGET_PYTHON="$work/venv/bin/python"

go build -o "$work/diagnose-qwen35" ./cmd/diagnose-qwen35
python3 -m venv "$work/venv"
"$BRIEFING_BUDGET_PYTHON" -m pip install -r tools/context_budget/requirements.txt
# Metadata only; never generate here or download model weights.
curl --fail --silent --show-error --max-time 60 "$OLLAMA_BASE_URL/api/show" \
  -H 'Content-Type: application/json' -d '{"model":"qwen3.5:9b","verbose":true}' > "$work/show.json"
curl --fail --silent --show-error --max-time 30 "$OLLAMA_BASE_URL/api/tags" > "$work/tags.json"
curl --fail --silent --show-error --max-time 30 "$OLLAMA_BASE_URL/api/version" > "$work/version.json"
"$BRIEFING_BUDGET_PYTHON" -c 'import json,sys; print("ollama version is " + json.load(open(sys.argv[1]))["version"])' \
  "$work/version.json" > "$work/version.txt"
"$BRIEFING_BUDGET_PYTHON" tools/context_budget/prepare_qwen35.py \
  --manifest internal/diagnostic/profiles/qwen35-9b-v1.json \
  --show "$work/show.json" --tags "$work/tags.json" --version "$work/version.txt" \
  --download --output "$work/resources"

args=(--profile qwen35-9b-summary-v2-v1 --profile-resources "$work/resources" --mode all)
# All 18 budgets locally first; no network in the plan itself.
"$work/diagnose-qwen35" "${args[@]}" --case six --out "$work/plan.jsonl"
# Explicit metadata/exact renderer checks for all three modes; zero generations.
"$work/diagnose-qwen35" "${args[@]}" --case sr-01 --check-profile --out "$work/check.jsonl"
# One pass only: six cases x three modes, sequentially. No fallback/retry/repair.
"$work/diagnose-qwen35" "${args[@]}" --case six --run --out "$work/comparison.jsonl"
