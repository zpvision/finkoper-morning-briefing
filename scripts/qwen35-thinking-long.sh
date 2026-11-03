#!/usr/bin/env bash
# Explicit single probe or 18 regressions; no service control or automatic rerun.
set -Eeuo pipefail
export PATH="/snap/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"
export CGO_ENABLED=0
umask 077
cd "$(dirname "${BASH_SOURCE[0]}")/.."
selected=${1:-sr-06}
if [[ $# -gt 1 || ( "$selected" != sr-06 && "$selected" != regressions ) ]]; then
  printf 'Usage: bash scripts/qwen35-thinking-long.sh [sr-06|regressions]\n' >&2
  exit 2
fi
result_name=result.jsonl
if [[ "$selected" == regressions ]]; then result_name=results.jsonl; fi
work=$(mktemp -d "$HOME/qwen35-thinking-long.XXXXXX")
finish() {
  code=$?
  printf '\nReport: %s/%s\nDirectory: %s\nExit code: %s\n' "$work" "$result_name" "$work" "$code"
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

# Metadata only, no generation or model weight download.
curl --fail --silent --show-error --max-time 60 "$OLLAMA_BASE_URL/api/show" \
  -H 'Content-Type: application/json' -d '{"model":"qwen3.5:9b","verbose":true}' > "$work/show.json"
curl --fail --silent --show-error --max-time 30 "$OLLAMA_BASE_URL/api/tags" > "$work/tags.json"
curl --fail --silent --show-error --max-time 30 "$OLLAMA_BASE_URL/api/version" > "$work/version.json"
"$BRIEFING_BUDGET_PYTHON" -c 'import json,sys; print("ollama version is " + json.load(open(sys.argv[1]))["version"])' \
  "$work/version.json" > "$work/version.txt"

resources=/home/user85037/qwen35-comparison-reports.quKuMI/resources
if [[ ! -e "$resources" ]]; then
  if [[ "$selected" == regressions ]]; then
    printf 'Existing regression resources are required: %s\n' "$resources" >&2
    exit 1
  fi
  resources="$work/resources"
  "$BRIEFING_BUDGET_PYTHON" tools/context_budget/prepare_qwen35.py \
    --manifest internal/diagnostic/profiles/qwen35-9b-v1.json \
    --show "$work/show.json" --tags "$work/tags.json" --version "$work/version.txt" \
    --download --output "$resources"
fi
# Always verify using this checkout's new tool. Corruption stops; no repair/fallback.
"$BRIEFING_BUDGET_PYTHON" tools/context_budget/verify_qwen35.py \
  --resources "$resources" --show "$work/show.json" --tags "$work/tags.json" \
  --version "$work/version.txt" > "$work/resources-check.json"

args=(--profile qwen35-9b-summary-v2-v1 --profile-resources "$resources" --mode thinking-long)
if [[ "$selected" == regressions ]]; then
  # A budget refusal is recorded per input; allow the remaining inputs, never rerun.
  plan_status=0
  "$work/diagnose-qwen35" "${args[@]}" --case regressions --out "$work/plan.jsonl" || plan_status=$?
  "$BRIEFING_BUDGET_PYTHON" - "$work/plan.jsonl" "$plan_status" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as file:
    records = [json.loads(line) for line in file]
expected = [f"sr-{i:02d}" for i in range(1, 9)] + [f"hol-{i:02d}" for i in range(1, 11)]
valid = ([r.get("case_id") for r in records] == expected and
         all(r.get("generation_mode") == "thinking-long" and r.get("generation_attempts") == 0 and
             r.get("error") in (None, "input_budget_exceeded") and
             r.get("structural_validation_error") is None for r in records))
expected_status = int(any(r.get("error") == "input_budget_exceeded" for r in records))
if not valid or int(sys.argv[2]) != expected_status:
    raise SystemExit("Regression plan failed; see plan.jsonl. No generation started.")
PY
else
  "$work/diagnose-qwen35" "${args[@]}" --case "$selected" --out "$work/plan.jsonl"
fi
# Zero generations. Each generated input is checked again in the sole run below.
"$work/diagnose-qwen35" "${args[@]}" --case sr-06 --check-profile --out "$work/check.jsonl"
# One invocation per eligible input: at most 18; no retries/repair/fallback.
"$work/diagnose-qwen35" "${args[@]}" --case "$selected" --run --out "$work/$result_name"
