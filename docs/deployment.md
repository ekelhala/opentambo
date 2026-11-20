# Deployment

OpenTambo — a lightweight llama.cpp process manager and persistent KV-cache
proxy with a web control panel — runs as the user who owns the models, without
sudo. The examples below use an inference host where the models live on a
mounted disk. (A standard automated system-wide installation with a dedicated
service account is planned, see #16.)

## systemd

Review [`deploy/opentambo.service`](../deploy/opentambo.service) and adjust
the user, paths, and model mount before installing it. Check the paths and
model mount first; `ExecStartPre` refuses to start when the mount is missing.

```sh
sudo cp deploy/opentambo.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now opentambo
```

The unit stops cleanly (`KillMode=control-group`): managed `llama-server`
children are in OpenTambo's own process groups and go down with it. Children
do not restart automatically when OpenTambo restarts; start the models you
need from the UI.

A server the user started themselves on a port is not adopted or controlled —
stop it yourself before launching another on that port.

## Manual install

```sh
install -Dm755 opentambo ~/.local/bin/opentambo
```

Or use the one-line installer from the [README](../README.md), which fetches
the matching release archive for your OS and architecture and verifies its
SHA-256 checksum.

## Networking

The listener defaults to `127.0.0.1:8090` and should stay on loopback.
opentambo has **no authentication**; exposure is delegated to your access
policy (e.g. Pangolin with Newt on the same host). LiteLLM stacks that use
Linux host networking can reach the loopback listener directly.
