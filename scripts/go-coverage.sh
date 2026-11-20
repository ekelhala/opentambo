#!/usr/bin/env bash
# Go coverage collection and ratchet enforcement (issue #19).
#
# Native Go coverage measures *statements*, not literal source lines; all
# percentages reported here are statement coverage. The annotated per-line
# view lives in coverage.html.
#
# Usage:
#   scripts/go-coverage.sh              run the suite, write coverage.out, check floors
#   scripts/go-coverage.sh --check-only reuse an existing coverage.out (missing = fail)
#
# The floors in test/coverage-baseline.txt are a ratchet: raising them is
# normal, lowering one is a policy change that needs an explanation in the PR.
# A missing coverage artifact fails the check. Generated code and build
# output are excluded only through the documented rules in
# docs/testing.md ("Coverage denominators and exclusions").
set -euo pipefail
cd "$(dirname "$0")/.."

BASELINE=test/coverage-baseline.txt
PROFILE=coverage.out
MODULE=github.com/ekelhala/opentambo

if [[ ! -f "$BASELINE" ]]; then
  echo "coverage: missing $BASELINE (the ratchet file is required)" >&2
  exit 1
fi

if [[ "${1:-}" == "--check-only" ]]; then
  if [[ ! -f "$PROFILE" ]]; then
    echo "coverage: $PROFILE is missing; run 'make coverage' (missing artifacts fail)" >&2
    exit 1
  fi
else
  # -coverpkg=./... keeps every handwritten package in the denominator,
  # including ones a run happens not to exercise.
  go test -race -covermode=atomic -coverpkg=./... -coverprofile="$PROFILE" ./... >/dev/null
fi

total=$(go tool cover -func="$PROFILE" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')
echo "coverage: module statement coverage ${total}% (profile: $PROFILE)"

# Package-set cross-check: every built package that owns statements must
# appear in the profile, so a handwritten package cannot silently vanish
# from the denominator through a build-tag or spelling change. Generated
# packages appear in `go list` too; they carry their own exclusion below.
fail=0
for dir in $(go list ./cmd/... ./internal/... ./tools/... 2>/dev/null); do
  rel=${dir#"$MODULE"}
  rel=${rel#/}
  case "$rel" in
  internal/desk/api) continue ;; # generated wire types (issue #14)
  esac
  if [[ -n "$(find "${dir#github.com/ekelhala/opentambo/}" -maxdepth 1 -name '*.go' ! -name '*_test.go' 2>/dev/null)" ]]; then
    if ! grep -q "^$dir:" "$PROFILE"; then
      echo "coverage: FAIL package $rel has no profile blocks; exclusions need documented rules" >&2
      fail=1
    fi
  fi
done

# Per-package floors from the baseline file. Format (one rule per line):
#   module <floor>
#   package <dir> <floor>
while read -r kind name floor; do
  [[ "$kind" == "#"* || -z "$kind" ]] && continue
  case "$kind" in
  module)
    have=$total
    want=$name
    label="module total"
    ;;
  package)
    have=$(awk -v mod="$MODULE" -v pkg="$name" 'NR > 1 {
      split($0, a, " "); split(a[1], loc, ":"); file = loc[1]
      sub(mod "/?", "", file); sub(/\/[^/]*\.go$/, "", file)
      if (file != pkg) next
      stmts = a[2] + 0; count = a[3] + 0
      total_stmts += stmts
      if (count > 0) covered += stmts
    } END {
      if (total_stmts == 0) print "0.0"
      else printf "%.1f", 100 * covered / total_stmts
    }' "$PROFILE")
    want=$floor
    label="package $name"
    ;;
  *)
    echo "coverage: unparsable baseline rule: $kind $name $floor" >&2
    fail=1
    continue
    ;;
  esac
  if awk -v have="$have" -v want="$want" 'BEGIN { exit !(have + 0 < want + 0) }'; then
    echo "coverage: FAIL $label at ${have}% is below the ${want}% floor" >&2
    fail=1
  else
    echo "coverage: ok $label at ${have}% >= ${want}%"
  fi
done < <(grep -vE '^\s*(#|$)' "$BASELINE")

if [[ $fail -ne 0 ]]; then
  echo "coverage: floors are a ratchet; raising them is normal, lowering requires a policy note (issue #19)" >&2
  exit 1
fi

go tool cover -html="$PROFILE" -o coverage.html
echo "coverage: annotated report at coverage.html"
