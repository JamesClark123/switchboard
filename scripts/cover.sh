#!/usr/bin/env bash
# Per-module coverage with the Rule VI 90% floor. Cross-package attribution via
# -coverpkg so a test exercising another package (registry via the manager, the
# client transport via a real server, etc.) is credited. Narrowly excluded:
# generated stubs (/gen), entrypoint mains (/cmd/), and E2E packages (-e2e).
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

floor="${COVER_FLOOR:-90}"
modules=(
  src/libs/switchboard-proto
  src/services/switchboardd
  src/apps/switchboard-tui
)

fail=0
for m in "${modules[@]}"; do
  echo ">> cover $m"
  pushd "$m" >/dev/null
  mapfile -t pkgs < <(go list ./... | grep -v -e '/gen$' -e '/cmd/' -e '\-e2e')
  if [ "${#pkgs[@]}" -eq 0 ]; then
    echo "   (no testable packages)"; popd >/dev/null; continue
  fi
  covpkgs=$(IFS=,; echo "${pkgs[*]}")
  # -count=1: avoid the test cache, which mis-merges multi-package coverage
  # profiles (each cached binary's union view wins, deflating the total).
  go test -count=1 -covermode=atomic -coverpkg="$covpkgs" -coverprofile=cover.out "${pkgs[@]}" >/dev/null
  pct=$(go tool cover -func=cover.out | awk '/^total:/ {gsub("%","",$3); print $3}')
  echo "   total coverage: ${pct}%"
  awk -v p="$pct" -v f="$floor" 'BEGIN{ if (p+0 < f+0){ printf("   FAIL: %.1f%% < %d%% floor\n", p, f); exit 1 } }' || fail=1
  popd >/dev/null
done
exit $fail
