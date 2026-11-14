//go:build !ui

package desk

import "embed"

// assets is empty without the `ui` build tag: the Vite build output in
// internal/desk/web is not committed. `make build` (and CI) build the UI and
// compile with -tags ui; dev builds serve the API while Vite serves the UI.
var assets embed.FS
