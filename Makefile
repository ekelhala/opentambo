BINARY  := llama-desk
BIN_DIR := bin
WEB_DIR := internal/desk/web

.PHONY: all build ui dev test clean

all: build

## build: production build — compile the web UI, then the binary with the UI embedded
build: ui
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

## ui: production build of the web UI into $(WEB_DIR), where //go:embed picks it up
ui: ui/node_modules
	cd ui && bun run build

ui/node_modules: ui/package.json ui/bun.lock
	cd ui && bun install --frozen-lockfile

## dev: Vite dev server, proxying /api and /v1 to a local llama-desk on 127.0.0.1:8090
dev: ui/node_modules
	cd ui && bun run dev

## test: Go tests
test:
	go vet ./...
	go test -race ./...

clean:
	rm -rf $(BIN_DIR)
	rm -rf $(WEB_DIR)/assets $(WEB_DIR)/index.html $(WEB_DIR)/favicon.svg
