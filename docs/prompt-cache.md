# Persistent prompt cache

Launching a server with **Persistent prompt cache** enabled (the launch form
checkbox, or `"prompt_cache": {"enabled": true}` on `POST /api/runs`) makes
opentambo save and restore `llama-server` KV slot state on disk, so a
conversation can resume without re-processing its whole prompt — even when
the request comes from a different application or after the server was
stopped and started again. Clients keep sending normal OpenAI-compatible
requests with their full history; nothing about the API changes.

## How it works

A cache-enabled run receives `--slot-save-path` in a private directory under
`--data/prompt-cache`, and all inference to that run is serialized through
one lease. Between requests the proxy compares the incoming prompt's tokens
(rendered and tokenized by the child itself through `/apply-template` and
`/tokenize`) against saved snapshots in the run's namespace. When a stored
prefix beats the live slot by a configurable margin, the snapshot is restored
and only the remaining tokens are evaluated. After each fully completed
response the new slot state is snapshotted atomically; snapshots of failed or
truncated responses are never published.

## Compatibility is proven, not assumed

Before serving, each run's cache passes a capability probe against the
actual child, model, and configuration: save a slot snapshot, erase the
slot, restore from disk, and send a continuation turn. The probe only
classifies the cache `ready` when the backend's own counter
(`timings.cache_n`) shows the erased-then-restored slot measurably skipping
prompt work. Erasing before the restore is what makes the evidence honest —
reuse can come only from the snapshot, never from a still-warm slot.

Because capability is decided by measurement, there is no list of supported
architectures to maintain: whatever `llama-server` can actually save,
restore, and reuse works, including hybrid/recurrent models (Qwen3-Next-
style SSM hybrids and friends), and combinations where restore is
silently a no-op are classified `unsupported` instead of pretending to be
warm. Snapshots are keyed by a namespace hash of the model file contents
(all shards), the llama-server binary, and state-affecting settings such
as context size and GPU layers — never by the alias alone. Changing those
settings yields a fresh namespace.

## Scope and boundaries

- Opt-in only. Runs without the option behave exactly as before, and a run
  whose capability probe fails (see the **Prompt cache** panel: `no cache`)
  keeps the plain inference path.
- `/v1/chat/completions` and `/v1/completions`, one completion per request,
  text-only, single slot (slot 0). Streaming and tool-calling chat are
  supported. Embeddings, `/v1/responses`, and parallel `n > 1` bypass cache
  lookup safely.
- `cache_prompt: false` in a request body is honored: the request runs
  normally without lookup or publication.
- Storage is managed automatically: a total disk budget, per-entry size
  limit, TTL, and LRU eviction, with in-flight entries pinned. Files and
  directories are owner-only; snapshot contents and prompt tokens are never
  logged or downloadable.
- Snapshot saves reserve the worst-case snapshot size for the whole write, so
  an accepted save can never push the namespace past its budget. The
  namespace is a rolling cache by default: when a new save no longer fits,
  admission reclaims room before refusing, in this order — expired entries
  and abandoned writes under the persisted policy, then the snapshots the
  incoming verified state **supersedes** (its indexed prompt tokens are a
  leading slice of the incoming prompt tokens), then remaining snapshots
  least-recently-used first with a deterministic entry-ID tie-break. Only
  the minimum victim prefix that covers the shortfall goes; the cache is
  never emptied toward a headroom target. Restores touch entries, so active
  conversations move to the fresh end of the order and are not the next
  victims. Pinned entries (live restores) and live write reservations are
  never reclaimed, and when the eligible victims cannot cover the shortfall
  the refusal deletes nothing. TTL disabled does not disable pressure
  eviction; existing budgets, per-entry limits, and TTL stay authoritative,
  and no configuration is needed for this behavior.
- A failed save still pays its eviction cost: snapshots already reclaimed to
  admit a write stay gone even if the backend save or publication then
  fails — there is no rollback that would temporarily exceed the budget.
  Actual removals are always counted; only capacity that reclamation could
  not admit counts as a budget refusal, while filesystem and metadata
  faults report as save failures without pretending to be a full cache.
- Every save problem is attributable: the cache panel counts budget
  refusals, superseded snapshots replaced at admission, cold snapshots
  evicted for pressure, and the run summary's `last_error` names the kind
  of the most recent failure (reservation refused, admission reclaim
  failed, oversized entry, backend save error, coverage mismatch,
  publication error). The next successful save clears it.
- Snapshots exist only to accelerate prompt processing. A missing, corrupt,
  or incompatible snapshot never breaks inference: the restore is skipped and
  the request runs cold. Cache maintenance errors never replace a successful
  response.
- Direct connections to a child's own port bypass this coordination; keep all
  traffic for a cache-enabled run on the opentambo gateway port.

## Management API

- `GET /api/cache` — namespaces, entries, and per-run stats.
- `DELETE /api/cache/{id}` — remove one entry.
- `POST /api/cache/prune` — drop expired entries.

The run state in `/api/state` and the server detail page show cache status,
restores, saves, and failures.
