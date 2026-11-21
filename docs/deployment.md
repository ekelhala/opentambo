# Deployment

OpenTambo — a lightweight llama.cpp process manager and persistent KV-cache
proxy with a web control panel — can run as the user who owns the models,
without sudo, or as a system service with its own unprivileged account.

## System install (Linux with systemd)

One command installs a standard system-wide layout as root:

```sh
sudo opentambo install
```

The installer provisions everything itself and is idempotent — re-running it
upgrades the binary and unit while preserving the service account,
configuration, models and database state:

| Resource | Location |
| --- | --- |
| Application binary | `/usr/local/bin/opentambo` (root-owned) |
| Service identity | system user/group `opentambo`, login shell `/usr/sbin/nologin` |
| Administrator configuration | `/etc/opentambo/` (plus the root-only ownership record `install.json`) |
| Durable state / SQLite | `/var/lib/opentambo/` (service-owned, mode 0750) |
| Managed model directory | `/var/lib/opentambo/models/` (override with `--models`) |
| systemd unit | `/etc/systemd/system/opentambo.service` (generated) |
| Logs | journald (`journalctl -u opentambo`) |

Nothing is inferred from the invoking user's `HOME`, so the result is the
same for any operator and any sudo configuration. Before changing the host
the installer refuses non-Linux systems, hosts without a running systemd,
missing native runtimes and conflicting pre-existing accounts — including a
pre-existing `opentambo` user with an interactive shell, which is never
silently repurposed. Override paths with `--llama-server`, `--models`,
`--data`, `--listen`, `--user`, `--group`, `--extra-groups render,video`
(GPU access) and `--mount /srv/models` (refuse to start when a model mount
is absent). To adopt state from an earlier per-user install:

```sh
sudo opentambo install --import-state ~/.local/share/opentambo
```

The import copies the SQLite database and cache metadata only (never model
weights), leaves the old installation intact for rollback, and refuses to
overwrite a live destination database — stop the old manager before starting
the service so two managers never run against one history.

The generated unit runs the manager (and its `llama-server` children) as
the service account with `ProtectSystem=full`, `ProtectHome=true`,
`PrivateTmp=true` and `ReadWritePaths=/var/lib/opentambo`, bounded graceful
shutdown (`TimeoutStopSec=15`, `KillMode=control-group`) so managed
inference children always go down with the service.

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
- `--purge-all` — state, models and an empty `/etc/opentambo`; the service
  account is dropped only when it was created by the installer and
  everything it could own has been purged.

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
opentambo has **no authentication**; exposure is delegated to your access
policy (e.g. Pangolin with Newt on the same host). LiteLLM stacks that use
Linux host networking can reach the loopback listener directly.
