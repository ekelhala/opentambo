# AGENTS.md

Guidance for automated agents and contributors working in this repository.

## What this project is

opentambo is a single-binary local manager for `llama-server`: GGUF model
shelf, Hugging Face downloads, managed run lifecycle, an OpenAI-compatible
`/v1/*` gateway proxy, prompt-cache administration, and an embedded React UI.
Go backend (`internal/desk`), bun/React frontend (`ui`), single static binary
at release.

## Build and test

```sh
make test          # go vet + go test -race ./...
make generate-api  # regenerate wire types from api/openapi.yaml (Go + TS)
make build         # UI (bun) then binary with -tags ui into bin/opentambo
make dev           # backend on 127.0.0.1:8090 + Vite dev server
cd ui && bun run test && bun run lint && bunx tsc -b
```

`bun.lock` is canonical for the UI; `ui/node_modules` and
`internal/desk/web` are git-ignored. Go 1.27+, bun, GNU make.

## API contracts (important)

`api/openapi.yaml` is the **authoritative** contract for every HTTP surface
opentambo owns: `/api/*`, `/health`, `/healthz`.

- **Never hand-edit generated files.** `internal/desk/api/gen.go` (oapi-codegen
  v2.8.2, config `api/oapi-codegen.yaml`) and `ui/src/lib/api.gen.ts`
  (openapi-typescript 7.13.0, isolated in `tools/tsgen/`) are regenerated with
  `make generate-api` and committed. Regeneration is byte-stable; a diff means
  the spec changed.
- **Never add schema for `/v1/*`.** The OpenAI-compatible proxy is governed by
  the external OpenAI contract (arbitrary fields, SSE streaming, upstream
  errors) and must stay out of `api/openapi.yaml`.
- Handler wire types in `internal/desk` are aliases/embeds of the generated
  records. To change a response shape, change the spec first, then
  `make generate-api`, then handlers. Frontend imports transport types through
  `ui/src/lib/api.ts`, which re-exports the generated ones.
- Enum discipline: `JobStatus`, `RunStatus`, `CacheStatus`
  (`checking | ready | unsupported | error`) and the three-shape `RunHealth`
  union come from the spec. Frontend keeps exhaustive `switch` coverage with
  `never` guards (see `ui/src/components/cache-status.tsx`) — a new enum
  member must break the build until rendered.
- Errors on owned routes are always `{"error": "..."}` (spec schema `Error`),
  including routing misses and guard rejections.
- Runtime conformance lives in `internal/desk/contract_test.go` (kin-openapi):
  live handler responses are validated against the spec; extend it when
  adding routes.

Full workflow and rationale: [docs/api-contracts.md](docs/api-contracts.md).

## Conventions

- Conventional Commits for all changes (`feat:`, `fix:`, `refactor:`,
  `docs:`, `test:`, `build:`, `ci:`), imperative subject, body explains why.
- No authentication by design: the manager binds locally; access control
  belongs to the operator's reverse proxy. Mutations are guarded against
  cross-origin requests (`Sec-Fetch-Site`/`Origin` checks in
  `internal/desk/app.go`).
- Request bodies on owned routes are decoded with `DisallowUnknownFields` and
  a 64 KiB limit; keep that when adding endpoints.
- The shipped binary must stay CGO-free and generator-free: `CGO_ENABLED=0
go build -tags ui`.

## CI

`.github/workflows/build.yml` runs: `go vet`/`go test -race`/gofmt; spec
regeneration with a fail-on-diff gate; UI tests, lint and `tsc -b`;
multi-platform release builds. Keep CI green for all of these.
