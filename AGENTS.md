# AGENTS.md

Guidance for automated agents and contributors working in this repository.

## What this project is

OpenTambo — a lightweight llama.cpp process manager and persistent KV-cache
proxy with a web control panel. Single-binary local manager for
`llama-server`: GGUF model shelf, Hugging Face downloads, managed run
lifecycle, an OpenAI-compatible `/v1/*` gateway proxy, prompt-cache
administration, and an embedded React UI. Go backend (`internal/desk`),
bun/React frontend (`ui`), single static binary at release. In prose use the
product name **OpenTambo**; keep lowercase identifiers (`opentambo`
executable, module paths, config keys, env vars) unchanged.

## Build and test

```sh
make test          # go vet + go test -race ./...
make check         # every CI gate, in CI order (fmt, lint, tests, coverage)
make generate-api  # regenerate wire types from api/openapi.yaml (Go + TS)
make build         # UI (bun) then binary with -tags ui into bin/opentambo
make dev           # backend on 127.0.0.1:8090 + Vite dev server
cd ui && bun run test && bun run lint && bunx tsc -b
```

`bun.lock` is canonical for the UI; `ui/node_modules` and
`internal/desk/web` are git-ignored. Go 1.27+, bun, GNU make.

## Quality gates (important)

- Coverage floors in `test/coverage-baseline.txt` are a **ratchet**: raising
  them is normal; lowering one, deleting a rule, or excluding a package
  requires a policy note in the PR description. A missing coverage artifact
  fails the gate — never paper over it.
- New or changed functions must stay under cyclomatic complexity 15 and
  cognitive complexity 20 (`cyclop`/`gocognit` in `.golangci.yml`). Legacy
  violations sit in an explicit text-pinned baseline; fix a violation by
  removing its baseline entry, never by adding to it.
- Do not weaken thresholds, broaden lint suppressions, or replace required
  real-backend integrations with mocks to make a gate pass.
- `make test-integration` runs the real OpenTambo binary (built with
  `-cover`) against a pinned real llama.cpp server on CPU. Zero executed
  integration tests fail the suite; there is no skip path.
- Full gate list, coverage denominators/exclusions and the integration
  scenario inventory: [docs/testing.md](docs/testing.md).

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
- Durable structured state (jobs, profiles, run history, cache namespaces,
  snapshot metadata, import markers) lives in the embedded SQLite database at
  `<data>/opentambo.db`, accessed only through `internal/desk/store`. Store
  types stay wire-separated: handlers translate between `store` rows and API
  records, never leak rows to the UI. GGUF models and KV snapshot blobs stay
  ordinary files. The binary must stay CGO-free: the pure-Go driver is the
  only SQLite implementation.
- Privileged lifecycle (`opentambo install` / `opentambo uninstall`, issue
  #16) lives only in `internal/lifecycle`: standard layout under
  `/usr/local/bin`, `/etc/opentambo`, `/var/lib/opentambo` with a generated
  systemd unit and a root-only ownership record at
  `<etc>/opentambo/install.json`. Host effects go through the `Command`
  seam and `Config.Prefix` staging so tests stay hermetic; paths from the
  record are re-validated before any removal and purge never leaves the
  managed state tree. Lifecycle dispatch in `cmd/opentambo` runs before any
  application initialization, so uninstall works with a damaged database or
  absent model mount. Uninstall preserves state, models and configuration
  unless the operator passes explicit purge flags.
- The shipped binary must stay CGO-free and generator-free: `CGO_ENABLED=0
go build -tags ui`.

## CI

`.github/workflows/build.yml` runs: `go vet`/`go test -race`/gofmt; spec
regeneration with a fail-on-diff gate; UI tests, lint and `tsc -b`;
multi-platform release builds. Keep CI green for all of these.
