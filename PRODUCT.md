# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Primary: Emil — self-hosting operator running llama.cpp inference on his own Linux host. He downloads GGUF files from Hugging Face, launches and supervises `llama-server` processes, and exposes them through his LiteLLM gateway to his own clients. He works from the terminal and a browser on the same network, runs the panel as the user who owns the models, and already operates Pangolin/Newt for access control.

Secondary (confirmed, personal-first): homelab operators with a similar setup — their own Linux box, their own `llama-server` build, their own gateway. They should be able to adopt the panel from the README without Emil's help, but they are not a support commitment that overrides his workflow.

## Product Purpose

A small, unauthenticated web control panel for native `llama-server` processes: download GGUFs from Hugging Face, browse the on-disk model directory, launch/manage server processes with exact arguments and environment, and proxy OpenAI-compatible `/v1` traffic to the running processes. It exists to replace hand-managed terminal sessions with one self-hosted binary, and to give LiteLLM a stable inference entry point — saved API model names keep client routes fixed when a child server's port or model changes. Success is the inference host being operable entirely from the panel while the machine stays the user's own.

## Positioning

One Go binary with the UI embedded, no database, npm, Docker, or bundled inference runtime — it manages the user's own native `llama-server` build with full argument/env transparency instead of wrapping or hiding it, and doubles as a minimal OpenAI-compatible gateway that routes by the request's `model` field to managed processes. Neighboring stacks (Ollama, Open WebUI, serving platforms) either own the runtime or need heavy infrastructure; llama-desk owns only the process management and keeps the exact command visible.

## Operating Context

- Linux inference host (amd64/arm64). Building needs Go 1.25+; running needs only the binary and a working `llama-server` build.
- Listens on loopback (`127.0.0.1:8090` by default); security is delegated to the user's Pangolin access policy, with Newt tunneling when on another host.
- Models live in a mounted GGUF directory (`--models`); transfer staging and state live under `--data` (`state.json`, owner-only permissions, plaintext env overrides).
- LiteLLM is the upstream gateway that owns client keys, policies, and its own database. llama-desk does not sync its model list into LiteLLM; routes are registered there once per API model name.
- Panel workflows map 1:1 to README sections: Downloads (queue GGUFs by repo + filename), Models (scan, inspect, launch, delete), Servers (launch form, PID/state/logs/health, stop-then-kill).
- Distribution: GitHub Actions artifacts and `v*` GitHub Releases; optional systemd unit at `deploy/llama-desk.service`.

## Capabilities and Constraints

Confirmed capabilities (first working slice):

- Downloads: public Hugging Face repos, exact GGUF filenames (multi-line queue), optional revision; bounded parallel transfers, byte progress and speed, cancel/resume with ETag/If-Range; `.part` until checks pass; existing files never replaced; no automatic retry.
- Models: recursive directory scan (symlinks excluded), path/size/mtime plus saved launch settings per file; explicit delete.
- Servers: launch with context, GPU layers, port, extra args (one argument per line, no shell evaluation), env overrides (`NAME=value`); PID, state, bounded live logs, explicit Health action, exact argument vector shown; graceful stop then kill after 5s; run-again reopens saved form.
- Gateway: `GET /v1/models` and `POST /v1/chat/completions|/v1/completions|/v1/embeddings|/v1/responses` proxied by `model` field; payload, SSE streams, and error statuses pass through; caller disconnect cancels upstream; 32 MiB request cap, 10-minute header wait, no total stream limit, no automatic retries; gateway auth headers/cookies stripped before forwarding.

Deliberate boundaries (documented in README; future work must not quietly cross them):

- No built-in authentication — intentional, delegated to Pangolin. The panel can delete models and execute the configured runtime with custom args/env; docs and UI state this plainly.
- No Hugging Face browser, gated-repo credentials, shard discovery, or upstream checksum verification; split models are launched from the first shard manually; vision needs a manual `--mmproj` path.
- GGUF inspection is filesystem metadata only, not parsed architecture/tensors.
- No GPU memory/temperature, slot activity, or throughput telemetry; no log persistence; no automatic eviction, model loading, or LiteLLM registration.
- Pre-existing `llama-server` on port 8080 is not adopted; managed processes bind to `127.0.0.1` only; one llama-desk instance per data directory; model servers do not auto-restart with the manager.

Architecture posture (user-confirmed "mostly, but flexible"): single Go binary + embedded UI, no database/npm/Docker, delegated auth, files-as-inventory are strong preferences, not vows — future work may add auth, a database, or dependencies when a concrete need justifies it.

Terminology: **models** = GGUF files on disk; **downloads** = queued Hugging Face jobs; **servers** = managed `llama-server` processes; **API model name/alias** = the stable gateway name passed to the child via `--alias`.

## Brand Commitments

- Name: **llama-desk** (GitHub `ekelhala/llama-desk`, MIT, © 2026 Emil Kelhala).
- Existing UI voice is plain and lightly warm: "Your models. Your server." / "A little room for your models." Numbered sections (01 Downloads…).
- No binding visual direction has been declared. The shipped scaffold and `mockup/llama-desk-ui.html` are recorded under Evidence as artifacts, not as an approved world; visual direction is a later new-work decision.

## Evidence on Hand

- `README.md` — complete product and API documentation: run flags, first-working-slice behavior, LiteLLM integration with example config, scaffold boundaries, build/release flow.
- `internal/desk/web/` — the current embedded UI scaffold (`index.html` 24 lines, `style.css` one line, `app.js` 40 lines): light green-tinted minimal look.
- `mockup/llama-desk-ui.html` — 1264-line standalone mockup, dark theme with amber accent; an uncommitted design exploration.
- `internal/desk/*.go` with tests (`app_test.go`, `proxy_test.go`); `.github/workflows/build.yml` runs tests and builds signed-archived linux amd64/arm64 releases.
- `deploy/llama-desk.service` — example systemd unit.
- Absent and not to be fabricated: testimonials, case studies, named users or customers, benchmarks, throughput numbers, press coverage.

## Product Principles

1. **The machine is the user's.** Manage their `llama-server` build and files as they are; show the exact argument vector and environment rather than abstracting them away.
2. **Personal-first, shareable.** Decide from Emil's real workflow first, but keep every step adoptable by an equivalent homelab operator from the README alone.
3. **Small by preference, flexible on evidence.** The single-binary, no-dependency posture is the default; departures need a demonstrated need, not a passing convenience.
4. **Power disclosed, security delegated.** The panel deletes files and executes commands; its surfaces say what that grants, and access control stays with Pangolin.
5. **Files are truth.** The model inventory is the on-disk directory; state and jobs supplement it and never contradict it.
