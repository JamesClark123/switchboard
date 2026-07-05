# Switchboard build/verify entry points. Mirrors the Rule V gate intent over the
# go.work workspace using the Go toolchain (see plan Constitution Check).
#
# The module list is explicit because `go.work` ties separate modules together;
# `go <cmd> ./...` only sees the current module, so we iterate.

MODULES := \
	src/libs/switchboard-proto \
	src/services/switchboardd \
	src/apps/switchboard-tui

E2E_MODULES := \
	src/apps/switchboard-tui-e2e \
	src/services/switchboardd-e2e

COVER_FLOOR := 90

.PHONY: all build lint vet test cover env-check proto fmt fmt-check e2e

all: fmt-check vet lint test

build:
	@for m in $(MODULES); do echo ">> build $$m"; (cd $$m && go build ./...) || exit 1; done

fmt:
	gofmt -w $$(find src -name '*.go' -not -path '*/gen/*')

fmt-check:
	@out=$$(gofmt -l $$(find src -name '*.go' -not -path '*/gen/*')); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	@for m in $(MODULES); do echo ">> vet $$m"; (cd $$m && go vet ./...) || exit 1; done

lint:
	@for m in $(MODULES); do echo ">> lint $$m"; (cd $$m && golangci-lint run ./...) || exit 1; done

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
