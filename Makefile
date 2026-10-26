# Switchboard build/verify entry points. Mirrors the Rule V gate intent over the
# go.work workspace using the Go toolchain (see plan Constitution Check).
#
# The module list is explicit because `go.work` ties separate modules together;
# `go <cmd> ./...` only sees the current module, so we iterate.

MODULES := \
	src/libs/switchboard-proto \
	src/libs/switchboard-dial \
	src/services/switchboardd \
	src/apps/switchboard-tui

E2E_MODULES := \
	src/apps/switchboard-tui-e2e \
	src/services/switchboardd-e2e

# fmt/fmt-check cover every go.work module, read from go.work itself so a new
# module is formatted without editing this file (lint/vet/test still use
# MODULES; see SWI-11). `=` defers the go call to the targets that use it.
FMT_MODULES = $(shell go list -m -f '{{.Dir}}' | sed 's|^$(CURDIR)/||')

COVER_FLOOR := 90

# golangci-lint must be built with a Go at least as new as the modules' `go`
# directive (1.26): an older build refuses to load the config. CI installs exactly
# this version (see .github/workflows/ci.yml, which reads it via `make lint-version`).
GOLANGCI_LINT_VERSION := v2.15.0

.PHONY: all build lint lint-version vet test cover env-check proto fmt fmt-check require-golangci-lint e2e

all: fmt-check vet lint test

build:
	@for m in $(MODULES); do echo ">> build $$m"; (cd $$m && go build ./...) || exit 1; done

# Formatting goes through golangci-lint so it applies the same formatters
# (gofmt + goimports) that lint enforces. Generated stubs are skipped by the
# config's `generated: strict` exclusion.
fmt: require-golangci-lint
	@test -n "$(FMT_MODULES)" || { echo "fmt: no modules found (go list -m failed)"; exit 1; }
	@for m in $(FMT_MODULES); do echo ">> fmt $$m"; (cd $$m && golangci-lint fmt ./...) || { \
		echo "fmt failed; CI uses golangci-lint $(GOLANGCI_LINT_VERSION) (yours: $$(golangci-lint version 2>&1 | head -1))"; exit 1; }; done

# `golangci-lint fmt --diff` exits 1 for a diff and another non-zero code when
# the tool itself fails (e.g. an older build can't load the config). Only the
# latter names the CI version, and it stops at once: it would repeat per module.
fmt-check: require-golangci-lint
	@test -n "$(FMT_MODULES)" || { echo "fmt-check: no modules found (go list -m failed)"; exit 1; }
	@fail=0; for m in $(FMT_MODULES); do \
		out=$$(cd $$m && golangci-lint fmt --diff ./... 2>&1); rc=$$?; \
		if [ $$rc -eq 1 ]; then echo "formatting needed in $$m (run make fmt):"; echo "$$out"; fail=1; \
		elif [ $$rc -ne 0 ]; then echo "golangci-lint failed in $$m:"; echo "$$out"; \
			echo "CI uses golangci-lint $(GOLANGCI_LINT_VERSION) (yours: $$(golangci-lint version 2>&1 | head -1))"; exit 1; fi; \
	done; exit $$fail

require-golangci-lint:
	@command -v golangci-lint >/dev/null || { \
		echo "make fmt/fmt-check need golangci-lint $(GOLANGCI_LINT_VERSION) (see README › Prerequisites; make lint-version)"; exit 1; }

vet:
	@for m in $(MODULES); do echo ">> vet $$m"; (cd $$m && go vet ./...) || exit 1; done

# On failure, name the version CI uses: an older local build fails with
# "can't load config: the Go language version … is lower than the targeted Go version".
lint:
	@for m in $(MODULES); do echo ">> lint $$m"; (cd $$m && golangci-lint run ./...) || { \
		echo "lint failed; CI uses golangci-lint $(GOLANGCI_LINT_VERSION) (yours: $$(golangci-lint version 2>&1 | head -1))"; exit 1; }; done

lint-version:
	@echo $(GOLANGCI_LINT_VERSION)

test:
	@for m in $(MODULES); do echo ">> test $$m"; (cd $$m && go test ./...) || exit 1; done

# Coverage with the 90% floor preserved from Rule VI. Delegated to a bash script
# for reliable package-list handling (cross-package attribution via -coverpkg;
# generated stubs, entrypoint mains, and E2E packages narrowly excluded).
cover:
	@COVER_FLOOR=$(COVER_FLOOR) bash scripts/cover.sh

proto:
	cd src/libs/switchboard-proto && ./gen.sh

env-check:
	@bash scripts/env-check.sh

# E2E suites (Rule VI): TUI via PTY (stub sbx, runs anywhere); daemon against a
# real Docker+sbx runtime (skips when absent). Gated behind the `e2e` build tag.
e2e:
	@for m in $(E2E_MODULES); do echo ">> e2e $$m"; (cd $$m && go test -tags e2e ./...) || exit 1; done
