BINARY  := opentambo
BIN_DIR := bin
WEB_DIR := internal/desk/web

# Local overrides (see .env.example), e.g. OPENTAMBO_LISTEN=127.0.0.1:8091
# when the default port is taken. Exported so both the Go backend and the
# Vite dev server pick them up.
-include .env
export OPENTAMBO_LISTEN

.PHONY: all build ui dev test test-ui test-integration generate-api clean
.PHONY: fmt-check lint coverage coverage-check changed-coverage check

# Pinned code generators (dev/CI only; the shipped binary never depends on
# them). Both read the authoritative contract api/openapi.yaml.
OAPI_CODEGEN_VERSION := v2.8.2

# Pinned quality tooling (issue #19). golangci-lint embeds the Go version it
# was built with and refuses to run when that is lower than this module's go
# directive, so the toolchain used to build it is pinned to match go.mod.
# CI installs the identical version the same way.
GOLANGCI_LINT_VERSION := v2.15.0
GOLANGCI_GO_TOOLCHAIN := go1.27.0
PRETTIER := $(CURDIR)/ui/node_modules/.bin/prettier

all: build

## build: production build — compile the web UI, then the binary with the UI embedded
build: ui
	CGO_ENABLED=0 go build -tags ui -trimpath -ldflags "-s -w" -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

## ui: production build of the web UI into $(WEB_DIR); -tags ui embeds it
ui: ui/node_modules
	cd ui && bun run build

ui/node_modules: ui/package.json ui/bun.lock
	cd ui && bun install --frozen-lockfile

## dev: run the backend on 127.0.0.1:8090 plus the Vite dev server proxying /api and /v1 to it
dev: ui/node_modules
	CGO_ENABLED=0 go run ./cmd/$(BINARY) & \
	BACKEND=$$!; \
	trap 'kill $$BACKEND 2>/dev/null' EXIT; \
	cd ui && bun run dev

## generate-api: regenerate Go and TypeScript wire types from api/openapi.yaml
generate-api:
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) -config api/oapi-codegen.yaml api/openapi.yaml
	cd tools/tsgen && bun install --frozen-lockfile
	cd tools/tsgen && bun run generate

## test: Go tests
test:
	go vet ./...
	go test -race ./...

## test-ui: web component/unit tests (bun test)
test-ui: ui/node_modules
	cd ui && bun run test

## test-integration: real OpenTambo binary + real pinned llama-server CPU suite
test-integration:
	go test -tags integration -count=1 -timeout 40m ./test/integration/...

## fmt-check: gofmt + Prettier in check mode (never edits files)
fmt-check: ui/node_modules
	test -z "$$(gofmt -l cmd internal tools)"
	cd ui && bunx prettier --check .
	$(PRETTIER) --ignore-path .prettierignore --check --config .prettierrc.json \
		'*.md' '*.yml' 'docs/**/*.md' 'api/**/*.yaml' '.github/**/*.yml' 'ui/*.json'

## lint: pinned static analysis and complexity gates (Go + frontend)
lint: ui/node_modules
	go vet ./...
	GOTOOLCHAIN=$(GOLANGCI_GO_TOOLCHAIN) go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
	cd ui && bun run lint

## coverage: race+cover profile, annotated report, and baseline ratchet check
coverage:
	scripts/go-coverage.sh

## coverage-check: enforce the recorded coverage floors against coverage.out
coverage-check:
	scripts/go-coverage.sh --check-only

## changed-coverage: ≥90% coverage of changed executable Go lines vs a base ref
## The instrumented integration profile (coverage.integration.out) is merged
## when present: paths only a real process reaches, like main(), must be
## proven by executing them, not exempted from the denominator.
changed-coverage:
	go run ./tools/covdiff -base $(or $(BASE_REF),origin/main) coverage.out \
		$(if $(wildcard coverage.integration.out),coverage.integration.out,)

## check: everything CI requires, in the CI order
check: fmt-check lint test test-ui coverage changed-coverage

clean:
	rm -rf $(BIN_DIR)
	rm -rf $(WEB_DIR)/assets $(WEB_DIR)/index.html $(WEB_DIR)/favicon.svg
