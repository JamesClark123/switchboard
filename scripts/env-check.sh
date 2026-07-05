#!/usr/bin/env bash
# env:check — Rule VIII (adapted for Go). For each module that ships a config
# package reading env vars, this:
#   (a) runs the in-language lockstep test asserting the config schema key-set
#       equals the .env.example key-set,
#   (b) verifies .env.example is tracked by git,
#   (c) verifies .env is NOT tracked by git.
# Any drift exits non-zero.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

modules=(
  "src/services/switchboardd"
  "src/apps/switchboard-tui"
)

fail=0
for m in "${modules[@]}"; do
  if [ ! -f "$m/.env.example" ]; then
    continue
  fi
  echo ">> env:check $m"

  # (a) schema <-> example lockstep, asserted in Go.
  ( cd "$m" && go test ./internal/config/... -run TestEnvExampleLockstep ) || fail=1

  # (b) .env.example tracked.
  if ! git ls-files --error-unmatch "$m/.env.example" >/dev/null 2>&1; then
    echo "   FAIL: $m/.env.example is not tracked by git"
    fail=1
  fi

  # (c) .env must NOT be tracked.
  if git ls-files --error-unmatch "$m/.env" >/dev/null 2>&1; then
    echo "   FAIL: $m/.env is tracked by git (it must be gitignored)"
    fail=1
  fi
done

exit $fail
