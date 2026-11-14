# Development

## Prerequisites

- Go 1.27+
- [bun](https://bun.sh) for the web UI (npm works too, but `bun.lock` is canonical)
- GNU make

## Layout

```text
cmd/opentambo/        entry point: flags, HTTP server, graceful shutdown
internal/desk/        the whole backend: app, catalog, downloads, processes,
                      gateway proxy, prompt cache, repository state
internal/desk/web/    Vite build output (git-ignored), embedded at build time
ui/                   React + TypeScript + Vite web UI (bun)
scripts/install.sh    release installer used by the README one-liner
deploy/               example systemd unit
docs/                 documentation
mockup/               design-language mockups (historical)
```

## Make targets

| Target | What it does |
| --- | --- |
| `make build` | builds the UI with bun, then the binary with `-tags ui` into `bin/opentambo` |
| `make ui` | production build of the web UI into `internal/desk/web` |
| `make dev` | backend on `127.0.0.1:8090` plus the Vite dev server proxying `/api` and `/v1` |
| `make test` | `go vet ./...` and `go test -race ./...` |
| `make clean` | removes `bin/` and the web build output |

Copy `.env.example` to `.env` for local overrides (e.g. `OPENTAMBO_LISTEN`
when the default port is taken); make and the Vite dev server both load it.

## How embedding works

The web UI build output in `internal/desk/web` is **not committed**. The Go
`assets` filesystem is selected by build tag:

- `-tags ui` (`internal/desk/web_embed.go`): `//go:embed all:web` embeds the
  built UI. Used by `make build` and CI, which run the Vite build first.
- without the tag (`internal/desk/web_stub.go`): empty filesystem. `go build`
  and `go test ./...` work on a bare checkout; the binary serves the API only,
  which is exactly what `make dev` needs (Vite serves the UI).

## Verifying changes

```sh
make test
make build   # bin/opentambo with the embedded UI
```

## CI and releases

GitHub Actions (`build.yml`) tests on pushes/PRs, then builds Linux and macOS
binaries for amd64 and arm64 as tarballs with SHA-256 files. Pushing a `v*`
tag publishes them as a GitHub Release; the README one-liner in
`scripts/install.sh` installs from the latest release. No deployment happens
automatically.
