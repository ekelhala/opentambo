//go:build ui

package desk

import "embed"

// assets holds the built web UI from internal/desk/web. Release and `make
// build` compiles the UI with bun first and pass -tags ui; plain `go build`
// and `go test` run without the embedded UI (see web_stub.go).
//
//go:embed all:web
var assets embed.FS
