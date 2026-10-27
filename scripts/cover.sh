#!/usr/bin/env bash
# Per-module tests + coverage with the Rule VI 90% floor. Cross-package
# attribution via -coverpkg so a test exercising another package (registry via
# the manager, the client transport via a real server, etc.) is credited.
# Narrowly excluded: generated stubs (/gen) and E2E packages (-e2e) are neither
# tested nor measured; entrypoint mains (/cmd/) are tested but not measured.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

floor="${COVER_FLOOR:-90}"
modules=(
  src/libs/switchboard-proto
  src/libs/switchboard-dial
  src/services/switchboardd
  src/apps/switchboard-tui
)

log=""
trap 'rm -f "$log"' EXIT

failed=()
# module_failed records a failed module (and annotates it in GitHub Actions);
# the caller then moves on to the next module.
module_failed() {
  failed+=("$1")
  if [ "${GITHUB_ACTIONS:-}" = true ]; then
    echo "::error title=make cover::$2 in $1"
  fi
}

for m in "${modules[@]}"; do
  echo ">> cover $m"
  pushd "$m" >/dev/null
  # Listed first so a broken module fails here (set -e cannot see a failure
  # inside a process substitution).
  if ! all=$(go list ./...); then
    echo "   FAIL: go list failed in $m"
    module_failed "$m" "go list failed"; popd >/dev/null; continue
  fi
  pkgs=()
  covered=()
  for p in $all; do
    case "$p" in
      */gen | *-e2e*) ;;
      */cmd/*) pkgs+=("$p") ;;
      *) pkgs+=("$p"); covered+=("$p") ;;
    esac
  done
  if [ "${#covered[@]}" -eq 0 ]; then
    echo "   (no testable packages)"; popd >/dev/null; continue
  fi
  covpkgs=$(IFS=,; echo "${covered[*]}")
  # -count=1: avoid the test cache, which mis-merges multi-package coverage
  # profiles (each cached binary's union view wins, deflating the total).
  # Quiet on success; on failure print the output (without -v it holds only the
  # failing tests plus ok/FAIL lines) and still measure the remaining modules.
  # A failed module's cover.out may be partial, so it is not measured.
  log=$(mktemp)
  if ! go test -count=1 -covermode=atomic -coverpkg="$covpkgs" -coverprofile=cover.out "${pkgs[@]}" >"$log" 2>&1; then
    echo "   FAIL: tests failed in $m:"
    sed 's/^/   /' "$log"
    module_failed "$m" "tests failed"; popd >/dev/null; continue
  fi
  rm -f "$log"
  if ! pct=$(go tool cover -func=cover.out | awk '/^total:/ {gsub("%","",$3); print $3}'); then
    echo "   FAIL: go tool cover failed in $m"
    module_failed "$m" "go tool cover failed"; popd >/dev/null; continue
  fi
  echo "   total coverage: ${pct}%"
  if ! awk -v p="$pct" -v f="$floor" 'BEGIN{ if (p+0 < f+0){ printf("   FAIL: %.1f%% < %d%% floor\n", p, f); exit 1 } }'; then
    module_failed "$m" "coverage ${pct}% below the ${floor}% floor"
  fi
  popd >/dev/null
done
if [ "${#failed[@]}" -gt 0 ]; then
  echo ">> modules failed: ${failed[*]}"
  exit 1
fi
