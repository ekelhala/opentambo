# API contracts

The HTTP surface of OpenTambo is split into two categories with different
ownership rules. The split is deliberate and the CI enforces only the part
OpenTambo owns.

## Ownership rule

| Surface                           | Owner                                    | Contract                                                             |
| --------------------------------- | ---------------------------------------- | -------------------------------------------------------------------- |
| `/api/*`, `/health`, `/healthz`   | opentambo                                | [`api/openapi.yaml`](../api/openapi.yaml) — the authoritative schema |
| `/v1/*` (OpenAI-compatible proxy) | OpenAI's external compatibility contract | deliberately **not** in `api/openapi.yaml`                           |

The `/v1/*` proxy must forward arbitrary request fields, upstream error
shapes, streaming/SSE responses and cancellation semantics exactly as the
upstream `llama-server` and OpenAI clients expect. Binding it to a generated
management schema would constrain pass-through behavior, so schema
generation and validation never apply to it.

Everything opentambo owns — state, downloads, repository browsing, model
files, run lifecycle/logs/health, prompt-cache administration and service
health — is defined in `api/openapi.yaml` with the exact wire shapes the
handlers emit.

## Generated types

`api/openapi.yaml` is the single source of truth. Both sides of the wire get
types generated from it:

| Output                     | Generator (pinned)                                                                                                                                     | Command             |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------- |
| `internal/desk/api/gen.go` | [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen) v2.8.2, config `api/oapi-codegen.yaml`                                                    | `make generate-api` |
| `ui/src/lib/api.gen.ts`    | [openapi-typescript](https://github.com/openapi-typescript/openapi-typescript) 7.13.0 (isolated in `tools/tsgen/`, drives TypeScript 5's compiler API) | `make generate-api` |

Regenerate after any spec change and commit the output — the generators are
byte-stable, so a diff after regeneration means the spec changed. Never edit
generated files and never handwrite transport structs or UI interfaces that
duplicate them; extend the spec instead. The backend handler types
(`Job`, `Run`, `Launch`, `PromptCache`, catalog/repository/cache records)
are aliases or embeds of the generated records, so what the handlers compile
against _is_ the contract.

The toolchain stays a dev/CI dependency only: the shipped single binary
embeds nothing from the generators.

## Conventions enforced by the contract

- Errors on owned routes are always `{"error": "..."}` (`Error` schema) with
  the right status code, including routing misses (404/405) and guard
  rejections (403/415).
- Enums are closed sets with generated constants on both sides:
  `JobStatus`, `RunStatus`, `CacheStatus` (`checking | ready | unsupported |
error` — the drift fixed in #13 is now a contract, see below) and the
  three-shape `RunHealth` union.
- Optional collections (`Launch.args`, `Launch.env`) are always present on
  the wire; absent client values normalize to `[]`/`{}` on every round trip.

## How to keep it honest

Three layers, all in `make test` and CI:

1. **Schema validity** — `internal/desk/contract_test.go` loads
   `api/openapi.yaml` with [kin-openapi](https://github.com/getkin/kin-openapi)
   and fails if the document itself does not validate.
2. **Runtime conformance** — live handler responses (state snapshot, run
   lifecycle with a fake child process, cache report/prune/delete, error and
   routing-miss replies, launch-config round trip) are validated against the
   documented response schemas. A handler that drifts from the contract fails
   here even if no test asserts on that field. `Run.Launch` must round trip
   unchanged through launch → state → profile, and the Go enum constants must
   match the contract enumerations.
3. **Generation drift** — CI regenerates both outputs and fails on any diff
   (`git diff --exit-code`), so committed types can never lag the spec.

## Changing an owned contract

1. Edit `api/openapi.yaml` (schema, status codes, enums).
2. `make generate-api` and commit the regenerated files together with the spec.
3. Adjust handlers against the generated types; `make test` runs the
   runtime-conformance suite.
4. Frontend: import the generated types through `ui/src/lib/api.ts`;
   `cache-status.tsx` keeps exhaustive `switch` coverage over the generated
   `CacheStatus` union with `never` guards, so a new enum member breaks the
   build until it is rendered.

Related: [Development guide](development.md) · issue
[#14](https://github.com/ekelhala/opentambo/issues/14).
