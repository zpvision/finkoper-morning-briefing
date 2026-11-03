#!/usr/bin/env bash
# Operator-only comparison in a new HOME directory; never touches service installations.
set -euo pipefail
export PATH="/snap/bin:$PATH"
umask 077
commit=${1:?Usage: bash tools/run-summary-comparison.sh FULL_COMMIT [SET] [PAIR]}
selection=${2:-regressions}
pair=${3:-v2-v3}
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || { printf 'Expected full commit SHA\n' >&2; exit 2; }
case "$selection" in regressions|new-regressions|previous-holdout|development|examples) ;; *) exit 2 ;; esac
case "$pair" in v2-v3|v2-v4) ;; *) exit 2 ;; esac
for command_name in git go python3; do command -v "$command_name" >/dev/null; done
work=$(mktemp -d "$HOME/finkoper-summary-comparison.XXXXXX")
report="$work/comparison.jsonl"
finish() {
  run_exit=$?
  trap - EXIT
  printf 'commit=%s\nselection=%s\npair=%s\nreport=%s\nexit=%s\n' "$commit" "$selection" "$pair" "$report" "$run_exit"
  exit "$run_exit"
}
trap finish EXIT
git clone --no-checkout https://github.com/zpvision/finkoper-morning-briefing.git "$work/src"
git -C "$work/src" checkout --detach "$commit"
test "$(git -C "$work/src" rev-parse HEAD)" = "$commit"
cd "$work/src"
GOOS="$(go env GOHOSTOS)" GOARCH="$(go env GOHOSTARCH)" CGO_ENABLED=0 go build -o "$work/compare-summary" ./cmd/compare-summary
python3 -m venv "$work/venv"
"$work/venv/bin/python" -m pip --disable-pip-version-check install --only-binary=:all: --no-deps -r tools/context_budget/requirements.txt
mkdir "$work/tokenizer"
resource_source=/opt/finkoper-morning-briefing/resources/tokenizer/qwen3-8b
for name in manifest.json tokenizer.json tokenizer_config.json; do
  if [ -r "$resource_source/$name" ]; then
    cat -- "$resource_source/$name" > "$work/tokenizer/$name"
  else
    sudo cat -- "$resource_source/$name" > "$work/tokenizer/$name"
  fi
done
compare() {
  env BRIEFING_BUDGET_PYTHON="$work/venv/bin/python" \
    BRIEFING_BUDGET_HELPER="$work/src/tools/context_budget/helper.py" \
    BRIEFING_TOKENIZER_DIR="$work/tokenizer" BRIEFING_BUDGET_TIMEOUT=60s \
    OLLAMA_BASE_URL=http://127.0.0.1:11434 OLLAMA_MODEL=qwen3:8b \
    "$work/compare-summary" --pair "$pair" --set "$selection" "$@"
}
compare --out "$work/plan.jsonl"
status=0
compare --run --out "$report" || status=$?
exit "$status"
