#!/usr/bin/env bash
# One explicit sr-06 diagnostic probe; no service control or automatic rerun.
set -Eeuo pipefail
export PATH="/snap/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"
export CGO_ENABLED=0
umask 077
cd "$(dirname "${BASH_SOURCE[0]}")/.."
work=$(mktemp -d "$HOME/qwen35-thinking-long.XXXXXX")
finish() {
  code=$?
  printf '\nReport: %s/result.jsonl\nDirectory: %s\nExit code: %s\n' "$work" "$work" "$code"
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

args=(--profile qwen35-9b-summary-v2-v1 --profile-resources "$resources" --mode thinking-long --case sr-06)
"$work/diagnose-qwen35" "${args[@]}" --out "$work/plan.jsonl"
"$work/diagnose-qwen35" "${args[@]}" --check-profile --out "$work/check.jsonl"
# Exactly one allowed generation, only after all checks; no retries/repair/fallback.
"$work/diagnose-qwen35" "${args[@]}" --run --out "$work/result.jsonl"
