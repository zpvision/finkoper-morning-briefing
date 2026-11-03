#!/usr/bin/env bash
# Manual operator command only. No pull/update, service control or global configuration.
set -Eeuo pipefail
export PATH="/snap/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"
export CGO_ENABLED=0
umask 077
cd "$(dirname "${BASH_SOURCE[0]}")/.."
work=$(mktemp -d "$HOME/qwen35-9b-probe.XXXXXX")
finish() {
  code=$?
  printf '\nReport directory: %s\nExit code: %s\n' "$work" "$code"
}
trap finish EXIT
export OLLAMA_BASE_URL=http://127.0.0.1:11434
export OLLAMA_MODEL=qwen3.5:9b
export BRIEFING_BUDGET_TIMEOUT=60s
export BRIEFING_BUDGET_PYTHON="$work/venv/bin/python"

go build -o "$work/diagnose-qwen35" ./cmd/diagnose-qwen35
python3 -m venv "$work/venv"
"$BRIEFING_BUDGET_PYTHON" -m pip install -r tools/context_budget/requirements.txt

# Metadata only; these requests never generate a summary or download model weights.
curl --fail --silent --show-error --max-time 60 "$OLLAMA_BASE_URL/api/show" \
  -H 'Content-Type: application/json' -d '{"model":"qwen3.5:9b","verbose":true}' > "$work/show.json"
curl --fail --silent --show-error --max-time 30 "$OLLAMA_BASE_URL/api/tags" > "$work/tags.json"
curl --fail --silent --show-error --max-time 30 "$OLLAMA_BASE_URL/api/version" > "$work/version.json"
"$BRIEFING_BUDGET_PYTHON" -c 'import json,sys; print("ollama version is " + json.load(open(sys.argv[1]))["version"])' \
  "$work/version.json" > "$work/version.txt"

# Explicit download: pinned public tokenizer/source files only, in a separate directory.
"$BRIEFING_BUDGET_PYTHON" tools/context_budget/prepare_qwen35.py \
  --manifest internal/diagnostic/profiles/qwen35-9b-v1.json \
  --show "$work/show.json" --tags "$work/tags.json" --version "$work/version.txt" \
  --download --output "$work/resources"

args=(--profile qwen35-9b-summary-v2-v1 --profile-resources "$work/resources" --case sr-01)
"$work/diagnose-qwen35" "${args[@]}" --out "$work/plan.jsonl"
# Zero generations. Missing/mismatched rendered_template aborts before the probe.
"$work/diagnose-qwen35" "${args[@]}" --check-profile --out "$work/check.jsonl"
# Exactly one generation, with another preflight and runtime prompt_eval_count comparison.
"$work/diagnose-qwen35" "${args[@]}" --run --out "$work/probe.jsonl"

# Observations only; model loading is not proof of GPU residency or semantic quality.
ollama ps
nvidia-smi
