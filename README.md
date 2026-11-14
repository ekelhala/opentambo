# opentambo

**The waystation for your models.** A small, unauthenticated web control panel
for native [`llama-server`](https://github.com/ggml-org/llama.cpp) processes:
download GGUFs from Hugging Face, browse your model directory, launch and
supervise server processes, and route OpenAI-compatible `/v1` traffic to them.
One Go binary with the UI embedded — no database, npm, Docker, or bundled
inference runtime.

## Install (Linux and macOS)

```sh
curl -fsSL https://raw.githubusercontent.com/ekelhala/opentambo/main/scripts/install.sh | sh
```

This grabs the latest [release](https://github.com/ekelhala/opentambo/releases),
verifies its SHA-256 checksum, and installs the binary to
`~/.local/bin/opentambo`. Set `OPENTAMBO_INSTALL_DIR` or `OPENTAMBO_VERSION`
to override. Building from source instead: see
[docs/development.md](docs/development.md).

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

> **No authentication, by design.** The panel can delete models and execute
> your configured runtime with custom arguments and environment variables.
> Keep the listener on loopback and put it behind your own access policy
> (e.g. Pangolin/Newt).

## Documentation

- [Getting started](docs/getting-started.md) — downloads, models, servers, flags, state
- [LiteLLM gateway](docs/gateway.md) — one stable `/v1` entry point for all local models
- [Persistent prompt cache](docs/prompt-cache.md) — resume conversations without re-processing prompts
- [Deployment](docs/deployment.md) — systemd unit, manual installs
- [Development](docs/development.md) — repo layout, make targets, how embedding works
- [Known limitations](docs/limitations.md) — deliberate scaffold boundaries

## License

MIT — see [LICENSE](LICENSE).
