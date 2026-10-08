GO ?= go

# make sync REPO=<name> DIR=<checkout>: render the baseline into a checkout.
# make drift: compare every repository's main with the baseline (read-only).
REPO ?=
DIR ?=

.PHONY: all
all: build vet test

.PHONY: build
build:
	$(GO) build ./...

.PHONY: install
install:
	$(GO) install ./cmd/baseline

.PHONY: test
test:
	$(GO) test -race ./...

.PHONY: fmt
fmt: ## Format with gofumpt (the commit hook also runs the configured formatters).
	$(GO) tool gofumpt -w .

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: clean
clean:
	rm -f drift.md drift-full.md

.PHONY: check
check: vet test ## What CI runs.

.PHONY: sync
sync: ## Render the baseline into DIR for REPO.
	@test -n "$(REPO)" -a -n "$(DIR)" || { echo "usage: make sync REPO=<name> DIR=<checkout>"; exit 2; }
	$(GO) run ./cmd/baseline sync -repo "$(REPO)" -dir "$(DIR)"

.PHONY: drift
drift: ## Compare every repository's main with the baseline; full diffs in drift-full.md.
	GH_TOKEN="$${GH_TOKEN:-$$(gh auth token)}" $(GO) run ./cmd/baseline drift \
		-baseline-sha "$$(git rev-parse origin/main 2>/dev/null)" -o drift.md -diffs drift-full.md
	@cat drift.md

.PHONY: help
help:
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
