# Testing and quality gates (issue #19)

Everything CI enforces runs locally through the same commands:

| Command                 | What it gates                                                                      |
| ----------------------- | ---------------------------------------------------------------------------------- |
| `make fmt-check`        | gofmt for Go; pinned Prettier for docs, YAML, JSON and UI sources (check mode)     |
| `make lint`             | `go vet`, pinned golangci-lint (`.golangci.yml`), `oxlint` for the UI              |
| `make test`             | `go vet` plus `go test -race ./...`                                                |
| `make test-ui`          | Web UI unit tests (`bun test`)                                                     |
| `make test-integration` | Real OpenTambo binary + pinned real llama-server CPU suite (see below)             |
| `make coverage`         | Race+cover suite, annotated `coverage.html`, ratchet floors                        |
| `make changed-coverage` | ≥90% coverage of changed executable Go lines vs `BASE_REF` (default `origin/main`) |
| `make check`            | All of the above in CI order                                                       |

## Static analysis and complexity ceilings

golangci-lint is pinned in the Makefile (`GOLANGCI_LINT_VERSION`, built with a
toolchain pinned to `go.mod` because the tool refuses to run when its embedded
Go version trails the module's). New or changed functions must stay under a
cyclomatic complexity of 15 (`cyclop`) and a cognitive complexity of 20
(`gocognit`). Legacy violations live in an explicit, text-pinned baseline in
`.golangci.yml`: adding a function to the baseline is a reviewable policy
change, and fixing a baseline violation should remove its entry.

## Coverage denominators and exclusions

Go percentages are **statement coverage** (the native Go metric), not literal
source-line coverage; the annotated per-line view is `coverage.html`.

- Unit runs collect with `-coverpkg=./...`, so every handwritten package is in
  the denominator even when a particular run does not execute it.
  `scripts/go-coverage.sh` cross-checks the package set: a built package with
  handwritten statements that produces no profile blocks fails the gate.
- Exclusions exist only as documented rules, never as silence:
  - `internal/desk/api` — generated wire types (regenerated from
    `api/openapi.yaml`, covered by the contract conformance tests).
  - `tools/tsgen` — JS generator harness.
  - `*_test.go` and `test/integration/**` in the changed-line gate — the
    integration suite is verified by _executing_ it, and its own lines are
    not meaningful coverage targets.
- Floors in `test/coverage-baseline.txt` are a ratchet: raising them is
  normal, lowering one or deleting a rule requires a policy note in the PR.
  A missing `coverage.out` artifact fails the check outright.
- `tools/covdiff` intersects `git diff -U0` hunk headers with the coverage
  profile and fails when changed executable lines fall below 90%. When no
  eligible executable line changed it reports `N/A` and passes — a docs-only
  delta is not blocked by a meaningless 0/0.

## Real-backend integration suite

`make test-integration` (`go test -tags integration ./test/integration/...`)
compiles the OpenTambo binary **with `-cover`** and drives it as a subprocess
against a real, pinned llama.cpp server on CPU. Nothing is mocked for the
required scenarios and there is no skip path: a missing fixture or an
unavailable download fails the run.

Fixtures (pinned; corrupt caches are replaced, never trusted):

- llama.cpp release `b12531`, asset `llama-b12531-bin-ubuntu-x64.tar.gz`,
  SHA-256 `5c1bf73c8c338d7934bbe7b43827496685f69b2e2afce2de9c536f755dd94a20`.
  Downloaded into `$OPENTAMBO_FIXTURE_CACHE` (default
  `/tmp/opentambo-fixtures`) and extracted on first use.
- Model: `models/unsloth/SmolLM2-135M-Instruct-GGUF/main/SmolLM2-135M-Instruct-Q4_K_M.gguf`
  from this repository (full-attention `llama` architecture, CPU-fast).

Scenarios:

- Manager startup/readiness (`/healthz`, `/health`), state, routing-miss and
  request-validation error shapes, run launch/health/stop, gateway model
  listing, non-streaming completions, OpenAI-style error preservation.
- Streaming SSE end-to-end (`data:` events plus the `[DONE]` sentinel),
  mid-stream cancellation with recovery, and concurrent gateway traffic
  through a cache-serialized run.
- **KV-reuse evidence from backend counters**: an A/B/A sequence over a
  cache-enabled run proves reuse through `timings.cache_n` against a cold
  baseline (`llama.cpp` reports `prompt_n` as only the tokens it evaluated,
  so total prompt size comes from `usage.prompt_tokens`), cross-checked with
  `usage.prompt_tokens_details.cached_tokens` and the coordinator's own hit
  counters.
- Child restart and OpenTambo restart over the same data directory keep
  reusing the same namespace from disk snapshots.
- Corrupted snapshot files degrade to a normal uncached completion — never a
  failed request or a wedged run.
- An injected crippled server stub (no usable slot 0) proves the capability
  probe marks the cache `unsupported` while inference keeps flowing uncached.
  This stub covers only the fault-injection scenario; the reuse evidence
  itself always comes from the pinned real build.

Zero executed integration tests fail the suite: `TestMain` requires instrumented
subprocess profiles and merges them into `coverage.integration.out`.
