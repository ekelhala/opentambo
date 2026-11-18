BINARY  := opentambo
BIN_DIR := bin
WEB_DIR := internal/desk/web

# Local overrides (see .env.example), e.g. OPENTAMBO_LISTEN=127.0.0.1:8091
# when the default port is taken. Exported so both the Go backend and the
# Vite dev server pick them up.
-include .env
export OPENTAMBO_LISTEN

.PHONY: all build ui dev test generate-api clean

# Pinned code generators (dev/CI only; the shipped binary never depends on
# them). Both read the authoritative contract api/openapi.yaml.
OAPI_CODEGEN_VERSION := v2.8.2

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

clean:
	rm -rf $(BIN_DIR)
	rm -rf $(WEB_DIR)/assets $(WEB_DIR)/index.html $(WEB_DIR)/favicon.svg
