# Storage and persistence

OpenTambo keeps its durable structured state in an **embedded SQLite
database** — `<data>/opentambo.db`, by default `./data/opentambo.db`. The
driver is compiled into the single binary: no database service, no daemon
to install, no `sqlite3` executable required. One OpenTambo
process owns a data directory at a time; SQLite doesn't make it safe for two
managers to share one model or cache directory.

## What lives where

Durable metadata lives in tables; large payloads stay ordinary files that are
easy to inspect, back up next to the database, and delete without a migration:

| Data                                                             | Home                                            |
| ---------------------------------------------------------------- | ----------------------------------------------- |
| Download jobs, resume checkpoints, ETag/validators, errors       | `downloads` table                               |
| Saved launch profiles incl. full prompt-cache configuration      | `profiles` table                                |
| Run history (every managed process, live vs. historical)         | `runs` table                                    |
| Cache namespace identity + retained storage policy               | `cache_namespaces` table                        |
| Snapshot metadata: checksums, indexed token prefixes, timestamps | `cache_snapshots` table                         |
| Import/migration bookkeeping markers                             | `meta` table                                    |
| GGUF models                                                      | files under `--models`                          |
| KV snapshot blobs                                                | `<id>.bin` files under each namespace directory |

Profiles may contain environment secrets and snapshot rows contain token
prefixes (conversation data), so the database and its WAL sidecars are created
with owner-only (`0600`) permissions. Snapshot blobs are written `0600` as
well.

SQLite runs in WAL mode with `synchronous=NORMAL` and a five-second busy
timeout — the right tradeoff for a local single-writer manager. Download
progress checkpoints are written from the transfer goroutine (at most every two
seconds), so a crash loses at most a short window of byte counts — never job
identity or resume position beyond that window.

## Crash recovery

Snapshot publication is a small state machine, so an interrupted write can
never masquerade as committed state: a row is recorded as `publishing`, the
blob is written to a temp file, validated (size, checksum), renamed into
place, and only then the row is committed. Removals announce `deleting` before
touching the file. On startup OpenTambo reconciles rows against the
filesystem — interrupted publications and announced deletions are finished,
blobs with no committed row reference are removed, and rows whose file is
missing or the wrong size are dropped.

Run history is reconciled the same way. A recorded PID is never proof of a
supervised process — after a manager restart every row that was still live is
demoted to `stopped` with an explanatory error. That way history survives
restarts without ever falsely claiming a dead process is running. Managed model
servers are stopped with the manager and don't automatically restart.

## Upgrading from JSON files

Pre-SQLite installations are imported exactly once, at startup, inside one
transaction per source:

- `state.json` → downloads + profiles. The marker `import.state_json` records
  completion; the original is renamed `state.json.migrated` and never deleted.
- Each namespace's `index.json` / `policy.json` → cache rows, likewise with the
  originals kept aside as `*.migrated`. A retried import replaces rows rather
  than duplicating them; a corrupt legacy file aborts startup untouched, so
  repairing or moving the file and restarting is always safe.

Schema versions are tracked with `PRAGMA user_version` and migrated forward
automatically, one transaction per version. A database newer than the running
binary is rejected with a clear error rather than silently misread —
upgrade OpenTambo itself to open it.

## Backup and restore

Back up with the manager stopped, so the database, its WAL sidecars and the
blob files cannot change mid-copy:

```sh
systemctl stop opentambo
tar czf opentambo-backup.tgz data/ models/
```

Restore by unpacking both directories into the same place and starting the
manager again. Models are inventory, not state, so a backup without them is
still consistent — you just have to re-download what is missing. If you must
copy while the manager is running, use a snapshotting copy of the whole data
directory and treat it as best-effort.
