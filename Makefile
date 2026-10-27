# Switchboard build/verify entry points. Mirrors the Rule V gate intent over the
# go.work workspace using the Go toolchain (see plan Constitution Check).
#
# `go.work` ties separate modules together and `go <cmd> ./...` only sees the
# current module, so every target iterates over a module list.
#
# The lists are read from go.work itself (SWI-7, SWI-11), so a new module is
# picked up by every target without editing this file. The `use` paths are taken
# as written (repo-relative, a leading ./ and a trailing / dropped), not
# resolved, so the checkout itself may be a symlink or sit under a path with a
# space; module paths in go.work must not contain spaces (the lists are words).
# scripts/cover.sh reads go.work the same way when run directly. `=` defers the
# go call to the targets that use it.
#   GO_MODULES  every module: fmt, fmt-check, vet, lint (with the e2e tag, so
#               the E2E suites are vetted and linted too)
#   MODULES     every module but the E2E ones: build, test (and scripts/cover.sh)
#   E2E_MODULES the *-e2e modules: e2e
GO_MODULES  = $(shell go work edit -json | sed -n 's|.*"DiskPath": "\(\./\)\{0,1\}\(.*\)".*|\2|p' | sed 's|/$$||')
MODULES     = $(filter-out %-e2e,$(GO_MODULES))
E2E_MODULES = $(filter %-e2e,$(GO_MODULES))

# require-modules fails a target (named by $(1)) whose module list ($(2)) came
# out empty, e.g. under GOWORK=off: an empty loop would pass vacuously.
require-modules = test -n "$(2)" || { echo "$(1): no go.work modules found (GOWORK off, go.work unreadable, or none match)"; exit 1; }

COVER_FLOOR := 90

# golangci-lint must be built with a Go at least as new as the modules' `go`
# directive (1.26): an older build refuses to load the config. CI installs exactly
# this version (see .github/workflows/ci.yml, which reads it via `make lint-version`).
GOLANGCI_LINT_VERSION := v2.15.0

.PHONY: all build lint lint-version vet test cover env-check proto fmt fmt-check require-golangci-lint e2e

all: fmt-check vet lint test

build:
	@$(call require-modules,build,$(MODULES))
	@for m in $(MODULES); do echo ">> build $$m"; (cd $$m && go build ./...) || exit 1; done

# Formatting goes through golangci-lint so it applies the same formatters
# (gofmt + goimports) that lint enforces. Generated stubs are skipped by the
# config's `generated: strict` exclusion.
fmt: require-golangci-lint
	@$(call require-modules,fmt,$(GO_MODULES))
	@for m in $(GO_MODULES); do echo ">> fmt $$m"; (cd $$m && golangci-lint fmt ./...) || { \
		echo "fmt failed; CI uses golangci-lint $(GOLANGCI_LINT_VERSION) (yours: $$(golangci-lint version 2>&1 | head -1))"; exit 1; }; done

# `golangci-lint fmt --diff` exits 1 for a diff and another non-zero code when
# the tool itself fails (e.g. an older build can't load the config). Only the
# latter names the CI version, and it stops at once: it would repeat per module.
fmt-check: require-golangci-lint
	@$(call require-modules,fmt-check,$(GO_MODULES))
	@fail=0; for m in $(GO_MODULES); do \
		out=$$(cd $$m && golangci-lint fmt --diff ./... 2>&1); rc=$$?; \
		if [ $$rc -eq 1 ]; then echo "formatting needed in $$m (run make fmt):"; echo "$$out"; fail=1; \
		elif [ $$rc -ne 0 ]; then echo "golangci-lint failed in $$m:"; echo "$$out"; \
			echo "CI uses golangci-lint $(GOLANGCI_LINT_VERSION) (yours: $$(golangci-lint version 2>&1 | head -1))"; exit 1; fi; \
	done; exit $$fail

require-golangci-lint:
	@command -v golangci-lint >/dev/null || { \
		echo "make fmt/fmt-check need golangci-lint $(GOLANGCI_LINT_VERSION) (see README › Prerequisites; make lint-version)"; exit 1; }

# The e2e tag is harmless outside the E2E modules (only they have tagged files)
# and without it those modules compile to just doc.go.
vet:
	@$(call require-modules,vet,$(GO_MODULES))
	@for m in $(GO_MODULES); do echo ">> vet $$m"; (cd $$m && go vet -tags e2e ./...) || exit 1; done

# On failure, name the version CI uses: an older local build fails with
# "can't load config: the Go language version … is lower than the targeted Go version".
lint:
	@$(call require-modules,lint,$(GO_MODULES))
	@for m in $(GO_MODULES); do echo ">> lint $$m"; (cd $$m && golangci-lint run --build-tags e2e ./...) || { \
		echo "lint failed; CI uses golangci-lint $(GOLANGCI_LINT_VERSION) (yours: $$(golangci-lint version 2>&1 | head -1))"; exit 1; }; done

lint-version:
	@echo $(GOLANGCI_LINT_VERSION)

test:
	@$(call require-modules,test,$(MODULES))
	@for m in $(MODULES); do echo ">> test $$m"; (cd $$m && go test ./...) || exit 1; done

# Coverage with the 90% floor preserved from Rule VI. Delegated to a bash script
# for reliable package-list handling (cross-package attribution via -coverpkg;
# generated stubs, entrypoint mains, and E2E packages narrowly excluded).
cover:
	@$(call require-modules,cover,$(MODULES))
	@COVER_FLOOR=$(COVER_FLOOR) COVER_MODULES="$(MODULES)" bash scripts/cover.sh

proto:
	cd src/libs/switchboard-proto && ./gen.sh

env-check:
	@bash scripts/env-check.sh

# E2E suites (Rule VI): TUI via PTY (stub sbx, runs anywhere); daemon against a
# real Docker+sbx runtime (skips when absent). Gated behind the `e2e` build tag.
e2e:
	@$(call require-modules,e2e,$(E2E_MODULES))
	@for m in $(E2E_MODULES); do echo ">> e2e $$m"; (cd $$m && go test -tags e2e ./...) || exit 1; done
