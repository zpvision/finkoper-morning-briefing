#!/usr/bin/env bash
# Manual root diagnostics in the existing tmux qwen27. No service control or pull.
set -Eeuo pipefail
export PATH="/snap/bin:/usr/local/go/bin:$PATH"
export CGO_ENABLED=0
umask 077
cd "$(dirname "${BASH_SOURCE[0]}")/.."
action=${1:-probe}
if [[ "$action" != probe && "$action" != remaining ]]; then exit 2; fi
work=${2:?Pass a new HOME report directory for probe, or its existing path for remaining}
if [[ "$action" == probe ]]; then mkdir "$work"; fi
finish() { code=$?; printf '\nReports: %s\nExit code: %s\n' "$work" "$code"; }
trap finish EXIT
export OLLAMA_BASE_URL=http://127.0.0.1:11434
export OLLAMA_MODEL=qwen3.5:27b-q4_K_M
export BRIEFING_BUDGET_TIMEOUT=60s
export BRIEFING_BUDGET_PYTHON="$work/venv/bin/python"
args=(--profile qwen35-27b-summary-v2-v1 --profile-resources "$work/resources" --mode baseline)
if [[ "$action" == probe ]]; then
  git rev-parse HEAD > "$work/commit.txt"
  go build -o "$work/diagnose-qwen35" ./cmd/diagnose-qwen35
  python3 -m venv "$work/venv"
  "$BRIEFING_BUDGET_PYTHON" -m pip install -r tools/context_budget/requirements.txt
  curl --fail --silent --show-error --max-time 60 "$OLLAMA_BASE_URL/api/show" \
    -H 'Content-Type: application/json' -d '{"model":"qwen3.5:27b-q4_K_M","verbose":true}' > "$work/qwen27-show.json"
  curl --fail --silent --show-error --max-time 30 "$OLLAMA_BASE_URL/api/tags" > "$work/ollama-tags.json"
  curl --fail --silent --show-error --max-time 30 "$OLLAMA_BASE_URL/api/version" > "$work/ollama-version.json"
  # Same pinned tokenizer/source assets as 9B, separate manifest/directory, no weights.
  "$BRIEFING_BUDGET_PYTHON" tools/context_budget/prepare_qwen35_27.py \
    --manifest internal/diagnostic/profiles/qwen35-27b-v1.json \
    --show "$work/qwen27-show.json" --tags "$work/ollama-tags.json" \
    --version "$work/ollama-version.json" --download --output "$work/resources"
  "$work/diagnose-qwen35" "${args[@]}" --case six --out "$work/plan.jsonl"
  "$work/diagnose-qwen35" "${args[@]}" --case sr-06 --check-profile --out "$work/check.jsonl"
  "$work/diagnose-qwen35" "${args[@]}" --case sr-06 --run --out "$work/probe.jsonl"
else
  if [[ -z ${TMUX:-} || -z ${TMUX_PANE:-} ]] || [[ "$(tmux display-message -p -t "$TMUX_PANE" '#{session_name}')" != qwen27 ]]; then
    printf 'Run remaining manually inside existing tmux session qwen27.\n' >&2
    exit 1
  fi
  # Separate explicit operator invocation after inspecting the probe. No automatic continuation.
  test "$(git rev-parse HEAD)" = "$(cat "$work/commit.txt")"
  "$work/diagnose-qwen35" "${args[@]}" --case remaining --probe-report "$work/probe.jsonl" \
    --run --out "$work/remaining.jsonl"
fi
