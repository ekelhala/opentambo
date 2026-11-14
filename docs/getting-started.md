# Getting started

opentambo manages native `llama-server` processes for one model directory on
one host. It needs only the binary and your own working `llama-server` build.

## Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8090` | HTTP listen address (env `OPENTAMBO_LISTEN`) |
| `--models` | `./models` | model directory (must already be mounted when on a separate disk) |
| `--llama-server` | `llama-server` (PATH) | native llama-server executable to launch |
| `--data` | `./data` | job history, launch settings, prompt cache |
| `--downloads` | `3` | maximum parallel downloads |

## Downloads

Enter a Hugging Face repository (`owner/name`) and a GGUF filename, including
any subdirectory; multiple filenames on separate lines queue together.
Revision defaults to `main`, with an optional branch/tag/commit field.

Transfers run with bounded parallelism, byte progress, speed, cancel and
resume. Downloads remain `.part` files until size/header checks pass; resume
uses ETag and If-Range, and changed files restart from byte zero. Existing
files are never replaced.

The same can be queued over the API:

```sh
curl http://127.0.0.1:8090/api/downloads \
  -H 'Content-Type: application/json' \
  -d '{"repo":"ggml-org/models","files":["tinyllamas/stories260K.gguf"]}'
```

## Models

The model directory is scanned recursively (symlinks excluded). Each file
shows path, size, modification time, and saved launch settings. Existing
GGUFs appear automatically; deletion is always explicit.

## Servers

Launch a model with context size, GPU layers, port, extra arguments, and
environment settings. The server page shows PID, process state, bounded live
logs, an explicit Health action, and the exact argument vector and environment
overrides. Stop is graceful, then kill after five seconds; *Run again* reopens
the saved launch form.

Extra arguments use **one argument per line**, with each value on its own
line. There is no shell evaluation:

```text
--parallel
1
--jinja
```

Environment overrides use `NAME=value`, one per line, for example
`CUDA_VISIBLE_DEVICES=GPU-9682481c-...`. The model, API alias, host, and port
are owned by the manager — use arguments rather than `LLAMA_ARG_*` variables.
Managed inference processes bind to `127.0.0.1`.

## State

Files are the model inventory. Jobs and per-model settings are saved
atomically in `--data/state.json` with owner-only permissions; environment
overrides are stored there in plaintext. Interrupted downloads can be resumed
after a restart. Model servers are stopped with the manager and do not
automatically restart — see [deployment.md](deployment.md) to run opentambo
under systemd.
