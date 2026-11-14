# llama-desk

A small, unauthenticated web control panel for native `llama-server` processes.
One Go binary, with its HTML, CSS, and JavaScript embedded. No database, npm,
Docker, or inference runtime included.

## Run

Requires Linux and Go 1.25+ to build. The inference host only needs the resulting
binary and your own working `llama-server` build.

```sh
go build -o bin/llama-desk ./cmd/llama-desk
./bin/llama-desk \
  --models /srv/models/gguf \
  --llama-server /home/emil/llama.cpp/build/bin/llama-server \
  --data /home/emil/.local/share/llama-desk \
  --listen 127.0.0.1:8090 \
  --downloads 3
```

Open `http://127.0.0.1:8090`. Defaults use `./models`, `./data`, and `llama-server`
from PATH. The model directory must already be mounted when using a separate disk.
Run as the user who owns the models, without sudo. Do not run two llama-desk
instances against the same data directory.

This application intentionally has **no authentication**. Put it behind your
Pangolin access policy. Keep its listener on loopback when Newt runs on the same
host. It grants the ability to delete models and execute your configured runtime
with custom arguments and environment variables.

## First working slice

- **Downloads:** enter a Hugging Face repository (`owner/name`) and a GGUF filename,
  including any subdirectory. Enter multiple filenames on separate lines to queue
  them together. Revision defaults to `main`, with an optional branch/tag/commit field.
  Bounded parallel transfers, byte progress, speed, cancel and resume. Downloads
  remain `.part` files until size/header checks pass. Resume uses ETag and
  If-Range; changed files restart from byte zero. Existing files are never replaced.
- **Models:** scan the directory recursively; inspect file path, size, modification
  time and saved launch settings; run or explicitly delete a file. Existing GGUFs
  from the home-directory downloader appear automatically. Symlinks are excluded.
- **Servers:** launch your native binary with context, GPU layers, port, arguments,
  and environment settings. See PID, process state, bounded live logs, health,
  and the exact argument vector/environment overrides. Stop gracefully, then kill
  after five seconds if necessary. Run again opens the saved launch form.

Files are the model inventory. Jobs and per-model settings are saved atomically
in `--data/state.json` with owner-only permissions. Environment overrides are
stored there in plaintext. Interrupted jobs can be resumed after restart; model
servers are stopped with the manager and do not automatically restart.

Extra arguments use **one argument per line**, with each value on its own line.
There is no shell evaluation. For example:

```text
--parallel
1
--jinja
```

Environment overrides use `NAME=value`, one per line, for example
`CUDA_VISIBLE_DEVICES=GPU-9682481c-77d1-da9c-c6bc-0c8374984d36`.
The model, API alias, host, and port are owned by the manager. Use arguments rather than
`LLAMA_ARG_*` variables. Managed inference processes bind to `127.0.0.1`.

## One inference entry point for LiteLLM

```text
Client → LiteLLM (keys and policies) → llama-desk /v1 → selected llama-server
```

Set **API model name** in the launch form, for example `qwen3.8-27b`. It is saved
with the model's launch settings and passed to the child with `--alias`. Active
names must be unique. This name stays stable when you change the child's port.

llama-desk exposes these routes on its normal listener (default port 8090):

- `GET /v1/models`: model names for currently running managed processes.
- `POST /v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, and
  `/v1/responses`: route by the JSON `model` field to a managed process.

Requests preserve their JSON payload, including custom parameters. Responses,
HTTP error statuses and SSE streaming pass through. A disconnected caller cancels
the upstream request. The gateway's authorization headers and cookies are removed
before forwarding to a child. There are no automatic retries of inference requests.
Requests are limited to 32 MiB; the proxy waits up to ten minutes for response
headers and does not impose a total stream duration.

There is no implicit startup or fallback: start the desired model in the UI first.
A stopped or unknown model returns an OpenAI-style HTTP 404; an unreachable child
returns 502. A child that is still loading can return its own 503. Process names
appear in `/v1/models` while loading; listing a model is not a readiness guarantee.
Backend support determines whether a particular inference endpoint works.

In LiteLLM, use the same API base for every local model route:

```yaml
model_list:
  - model_name: qwen3.8-27b
    litellm_params:
      model: openai/qwen3.8-27b
      api_base: http://127.0.0.1:8090/v1
      api_key: local-llama-desk
```

The last value is a dummy provider key; llama-desk does not authenticate. Retain
LiteLLM's existing database, client keys, and access controls. The deployed
LiteLLM stack uses Linux host networking, so it can reach this loopback address.
Changing or adding child servers no longer requires changing backend URLs in
LiteLLM. Model names/access policies still need registration in LiteLLM; this
proxy does not synchronize its model list into LiteLLM's database.

This is the intended deployment path. Existing live LiteLLM routes remain unchanged
until llama-desk is installed and starts owning the native inference processes.

## Persistent prompt cache

Launching a server with **Persistent prompt cache** enabled (the launch form checkbox,
or `"prompt_cache": {"enabled": true}` on `POST /api/runs`) makes llama-desk save and
restore `llama-server` KV slot state on disk, so a conversation can resume without
re-processing its whole prompt when the request comes from a different application,
or after the server was stopped and started again. Clients keep sending normal
OpenAI-compatible requests with their full history; nothing about the API changes.

How it works: a cache-enabled run receives `--slot-save-path` in a private directory
under `--data/prompt-cache`, all inference to that run is serialized through one
lease, and between requests the proxy compares the incoming prompt's tokens (rendered
and tokenized by the child itself through `/apply-template` and `/tokenize`) against
saved snapshots in the run's namespace. When a stored prefix beats the live slot by a
configurable margin, the snapshot is restored and only the remaining tokens are
evaluated. After each fully completed response the new slot state is snapshotted
atomically; snapshots of failed or truncated responses are never published.

Notes and boundaries:

- Opt-in only. Runs without the option behave exactly as before, and a run whose
  capability probe fails (see the **Prompt cache** panel: `no cache`) keeps the
  plain inference path.
- Scope: `/v1/chat/completions` and `/v1/completions`, one completion per request,
  text-only, single slot (slot 0). Streaming and tool-calling chat are supported.
  Embeddings, `/v1/responses`, and parallel `n > 1` bypass cache lookup safely.
- Compatibility is proven, not assumed: before serving, each child build passes a
  save/restore round-trip and a token-parity probe with the actual model. Snapshots
  are keyed by a namespace hash of the model file contents (all shards), the
  llama-server binary, and state-affecting settings such as context size and GPU
  layers — never by the alias alone. Changing those settings yields a fresh namespace.
- `cache_prompt: false` in a request body is honored: the request runs normally
  without lookup or publication.
- Storage is managed automatically: a total disk budget, per-entry size limit, TTL,
  and LRU eviction, with in-flight entries pinned. Files and directories are
  owner-only; snapshot contents and prompt tokens are never logged or downloadable.
- Management API: `GET /api/cache` lists namespaces, entries, and per-run stats;
  `DELETE /api/cache/{id}` removes one entry; `POST /api/cache/prune` drops expired
  entries. The run state in `/api/state` and the server detail page show cache
  status, restores, saves, and failures.
- Snapshots exist only to accelerate prompt processing. A missing, corrupt, or
  incompatible snapshot never breaks inference: the restore is skipped and the
  request runs cold. Cache maintenance errors never replace a successful response.

Direct connections to a child's own port bypass this coordination; keep all traffic
for a cache-enabled run on the llama-desk gateway port.

## Deliberate scaffold boundaries

Downloads currently support public repositories and exact GGUF filenames. Add all shards for a
split model and launch the first one. For vision, download the projector and add
`--mmproj` plus its absolute path as separate arguments. There is no Hugging Face
browser, gated-repository credential handling, shard discovery, or upstream
checksum verification yet. GGUF inspection currently shows filesystem metadata,
not parsed architecture/tensor metadata. Downloads are not automatically retried;
failed jobs expose the error and can be resumed manually.

For example, enter repository `ggml-org/models` and filename
`tinyllamas/stories260K.gguf`. The corresponding API request is:

```sh
curl http://127.0.0.1:8090/api/downloads \
  -H 'Content-Type: application/json' \
  -d '{"repo":"ggml-org/models","files":["tinyllamas/stories260K.gguf"]}'
```

The API also accepts an optional `revision` string. The earlier `urls` array is
still accepted for compatibility; the web UI builds downloads from repo + filename.

The server panel distinguishes process status from the explicit Health action.
It does not yet collect GPU memory/temperature, slot activity, throughput, or
persist server logs. No automatic eviction, model loading, or LiteLLM
registration is performed. LiteLLM remains a separate gateway.

A user-started server already on port 8080 is not adopted or controlled. Stop it
yourself before launching another on that port, or choose a different port.
Native processes started by llama-desk stay in its own process groups.

## Build and release

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/llama-desk ./cmd/llama-desk
```

GitHub Actions tests on pushes/PRs, then builds Linux amd64 and arm64 archives
with SHA-256 files. Download them from the workflow's artifacts. Pushing a `v*`
tag publishes the archives as a GitHub Release. No deployment happens automatically.

On the inference host, a workflow artifact can be retrieved using `gh`:

```sh
gh run list --repo ekelhala/llama-desk --workflow build.yml
gh run download RUN_ID --repo ekelhala/llama-desk \
  --name llama-desk-linux-amd64 --dir /tmp/llama-desk-release
cd /tmp/llama-desk-release
sha256sum -c llama-desk-linux-amd64.tar.gz.sha256
tar -xzf llama-desk-linux-amd64.tar.gz
install -Dm755 llama-desk ~/.local/bin/llama-desk
```

Review [deploy/llama-desk.service](deploy/llama-desk.service) for an optional
systemd unit. Check the paths and model mount before installing it. This repository
does not change your inference server until you install and start it there.
