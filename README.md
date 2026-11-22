# opentambo

**The waystation for your models.** OpenTambo is a lightweight llama.cpp
process manager and persistent KV-cache proxy with a web control panel.
Configure, launch and supervise your own native
[`llama-server`](https://github.com/ggml-org/llama.cpp) processes; preserve
and restore computed inference state across conversation switches and process
restarts for supported model/build combinations; and operate the model
inventory, Hugging Face downloads, server lifecycle and cache management from
the browser. OpenAI-compatible `/v1` routing keeps client URLs stable. One Go
binary with the UI embedded and embedded SQLite for durable state — no
external database service, npm, Docker, or bundled inference runtime.

## Install (Linux and macOS)

Per-user install (Linux and macOS):

```sh
curl -fsSL https://raw.githubusercontent.com/ekelhala/opentambo/main/scripts/install.sh | sh
```

This grabs the latest [release](https://github.com/ekelhala/opentambo/releases),
verifies its SHA-256 checksum, and installs the binary to
`~/.local/bin/opentambo`. Set `OPENTAMBO_INSTALL_DIR` or `OPENTAMBO_VERSION`
to override. Building from source instead: see
[docs/development.md](docs/development.md).

System service install (Linux with systemd) — one automated command:

```sh
sudo opentambo install
```

Provisions the standard layout (root-owned binary in `/usr/local/bin`,
dedicated unprivileged `opentambo` account, generated hardened unit, state
in `/var/lib/opentambo`, journald logs), then `sudo opentambo uninstall`
removes the installation while preserving models, database state and
configuration. [Deployment](docs/deployment.md) documents the full layout,
overrides, state import from a per-user install, and the purge opt-ins.

## Run

```sh
opentambo \
  --models /srv/models/gguf \
  --llama-server ~/llama.cpp/build/bin/llama-server \
  --data ~/.local/share/opentambo
```

Open `http://127.0.0.1:8090`. Defaults are `./models`, `./data`, and
`llama-server` from PATH. Run it as the user who owns the models, without
sudo, and only one instance per data directory.

The same settings can live in a versioned YAML operator configuration —
`--config`, `OPENTAMBO_CONFIG`, or `/etc/opentambo/config.yaml` when it
exists — which also carries prompt-cache defaults and declarative launch
profiles; explicit flags win over the file. Validate any edit with
`opentambo config check`. [Deployment](docs/deployment.md) documents the
full schema, precedence and profile inheritance.

> **No authentication, by design.** The panel can delete models and execute
> your configured runtime with custom arguments and environment variables.
> Keep the listener on loopback and put it behind your own access policy
> (e.g. Pangolin/Newt).

## Documentation

- [Getting started](docs/getting-started.md) — downloads, models, servers, flags, state
- [Storage and persistence](docs/storage.md) — embedded SQLite layout, migration, backup/restore
- [LiteLLM gateway](docs/gateway.md) — one stable `/v1` entry point for all local models
- [Persistent prompt cache](docs/prompt-cache.md) — resume conversations without re-processing prompts
- [Deployment](docs/deployment.md) — systemd unit, manual installs
- [Development](docs/development.md) — repo layout, make targets, how embedding works
- [Known limitations](docs/limitations.md) — deliberate scaffold boundaries

## License

MIT — see [LICENSE](LICENSE).
