# Deployment

OpenTambo — a lightweight llama.cpp process manager and persistent KV-cache
proxy with a web control panel — can run as the user who owns the models
(no sudo needed) or as a system service with its own unprivileged account.

## System install (Linux with systemd)

One command installs a standard system-wide layout as root:

```sh
sudo opentambo install
```

The installer provisions everything itself and is idempotent — re-running it
upgrades the binary and unit while keeping the service account,
configuration, models and database state intact:

| Resource                    | Location                                                                          |
| --------------------------- | --------------------------------------------------------------------------------- |
| Application binary          | `/usr/local/bin/opentambo` (root-owned)                                           |
| Service identity            | system user/group `opentambo`, login shell `/usr/sbin/nologin`                    |
| Administrator configuration | `/etc/opentambo/config.yaml` (plus the root-only ownership record `install.json`) |
| Durable state / SQLite      | `/var/lib/opentambo/` (service-owned, mode 0750)                                  |
| Managed model directory     | `/var/lib/opentambo/models/` (override with `--models`)                           |
| systemd unit                | `/etc/systemd/system/opentambo.service` (generated)                               |
| Logs                        | journald (`journalctl -u opentambo`)                                              |

Nothing is inferred from the invoking user's `HOME`, so the result is the
same for any operator and any sudo configuration. Before touching the host
the installer refuses non-Linux systems, hosts without a running systemd,
missing native runtimes and conflicting pre-existing accounts — including a
pre-existing `opentambo` user with an interactive shell, which is never
silently repurposed. Override paths with `--llama-server`, `--models`,
`--data`, `--listen`, `--user`, `--group`, `--extra-groups render,video`
(GPU access) and `--mount /srv/models` (refuses to start when a model mount
is missing). To adopt state from an earlier per-user install:

```sh
sudo opentambo install --import-state ~/.local/share/opentambo
```

The import copies the SQLite database and cache metadata only (never model
weights), leaves the old installation intact for rollback, and refuses to
overwrite a live destination database. Stop the old manager before starting
the service so two managers never run against the same history.

The generated unit runs the manager (and its `llama-server` children) as
the service account with `ProtectSystem=full`, `ProtectHome=true`,
`PrivateTmp=true` and `ReadWritePaths=/var/lib/opentambo`, bounded graceful
shutdown (`TimeoutStopSec=15`, `KillMode=control-group`) so managed
inference children always go down with the service. The unit starts the
manager with `--config /etc/opentambo/config.yaml`, and its `ExecStartPre`
refuses to start when that file is not readable.

### Operator configuration (YAML)

The install seeds a starter `/etc/opentambo/config.yaml` **only when none
exists** — an operator-owned file is never overwritten, and the running
service never rewrites it: runtime changes from the UI (launched servers,
saved profiles, job and cache history) live in the SQLite database under
the data directory. The YAML file holds operator intent: listen address,
paths, runtime executable and environment, download concurrency, prompt
cache policy, launch defaults, and declarative named profiles.

```yaml
schema_version: 1

http:
  listen: '127.0.0.1:8091'

paths:
  data_dir: '/var/lib/opentambo'
  models_dir: '/var/lib/opentambo/models'

runtime:
  executable: '/opt/llama.cpp/bin/llama-server'
  env:
    CUDA_VISIBLE_DEVICES: '0'

downloads:
  parallel: 3

prompt_cache:
  max_total_size: '32GiB'
  defaults:
    enabled: false
    max_namespace_size: '4GiB'
    max_entry_size: '2GiB'
    ttl: '168h'
    min_reuse_tokens: 256

launch_defaults:
  context_size: 8192
  gpu_layers: 99
  args: ['--flash-attn']
  env: {}

profiles:
  local-chat:
    model: 'local/llama-Q4_K_M.gguf'
    port: 8080
    autostart: true
    context_size: 32768
    args: ['--parallel', '1', '--jinja']
    prompt_cache:
      enabled: true
      ttl: '336h'
```

The parser is strict — unknown fields, duplicate keys, explicit nulls and
mistyped values are rejected with the exact field path. Sizes need explicit
binary units (`B`, `KiB`, `MiB`, `GiB`, `TiB`); durations use Go syntax
(`30m`, `168h`). Profiles inherit `launch_defaults` (arguments replace,
environment merges) and the host cache defaults per field. `autostart:
true` launches the profile when the manager starts. A failing profile is
logged and never blocks the manager or its siblings.

Validate any edit with no side effects before restarting the service:

```sh
opentambo config check --config /etc/opentambo/config.yaml
```

File discovery is `--config`, then `OPENTAMBO_CONFIG`, then
`/etc/opentambo/config.yaml` when it exists; an explicitly selected
missing file is a startup error, while a manual run with no file anywhere
keeps the built-in defaults. Values layer bottom-up: built-in defaults,
then the YAML file, then `OPENTAMBO_LISTEN`, then explicit command-line
flags.

### Uninstall

```sh
sudo opentambo uninstall
```

Removes the service, unit, binary and ownership record while **preserving**
durable state, models and administrator configuration. It works without the
original checkout, install script, model mount or a healthy database.
Explicit opt-ins, checked against the installer's ownership record:

- `--purge` — also remove the managed state tree (SQLite database, caches);
  a model directory inside it is still retained unless you add
- `--purge-models` — and only when the installer itself created that
  directory; external model trees are never recursively deleted;
- `--purge-all` — state, models, and the `config.yaml` this installer
  seeded (a file that already existed at install time was never seeded,
  so it always survives); `/etc/opentambo` is removed once empty; the
  service account is dropped only when it was created by the installer
  and everything it could own has been purged.

## Per-user install (Linux and macOS)

```sh
install -Dm755 opentambo ~/.local/bin/opentambo
```

Or use the one-line installer from the [README](../README.md), which fetches
the matching release archive for your OS and architecture and verifies its
SHA-256 checksum.

For a custom per-user systemd service (models on a mounted disk, runtime in
a nonstandard path), review
[`deploy/opentambo.service`](../deploy/opentambo.service) and adjust the
user, paths, and model mount before installing it; `ExecStartPre` refuses to
start when the mount is missing. Children do not restart automatically when
OpenTambo restarts; start the models you need from the UI. A server the user
started themselves on a port is not adopted or controlled — stop it yourself
before launching another on that port.

## Networking

The listener defaults to `127.0.0.1:8090` and should stay on loopback.
opentambo has **no authentication**; exposure is delegated to your reverse
proxy or access management solution. LiteLLM stacks that use
Linux host networking can reach the loopback listener directly.
