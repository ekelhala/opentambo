#!/bin/sh
# Install the latest released opentambo binary (Linux or macOS, amd64/arm64).
#
#   curl -fsSL https://raw.githubusercontent.com/ekelhala/opentambo/main/scripts/install.sh | sh
#
# Per-user install into ~/.local/bin by default. On Linux with systemd, a
# system-wide service install (dedicated account, generated unit, journald):
#
#   curl -fsSL .../install.sh | sudo OPENTAMBO_SYSTEM=1 sh
#
# Environment:
#   OPENTAMBO_INSTALL_DIR  install directory (default: ~/.local/bin)
#   OPENTAMBO_VERSION      release tag to install (default: latest release)
#   OPENTAMBO_SYSTEM       "1" = system service install (root, Linux, systemd)
#   OPENTAMBO_INSTALL_ARGS extra arguments passed to "opentambo install"
set -eu

REPO="ekelhala/opentambo"
BIN="opentambo"
SYSTEM="${OPENTAMBO_SYSTEM:-0}"
INSTALL_DIR="${OPENTAMBO_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'opentambo-install: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

os="$(uname -s)"
case "$os" in
  Linux) goos=linux ;;
  Darwin) goos=darwin ;;
  *) die "unsupported operating system: $os (need Linux or macOS)" ;;
esac
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) goarch=amd64 ;;
  arm64|aarch64) goarch=arm64 ;;
  *) die "unsupported architecture: $arch (need amd64 or arm64)" ;;
esac

version="${OPENTAMBO_VERSION:-}"
if [ -z "$version" ]; then
  version="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##')"
  [ -n "$version" ] || die "could not resolve the latest release of $REPO"
fi

asset="$BIN-$goos-$goarch.tar.gz"
base="https://github.com/$REPO/releases/download/$version"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "installing $BIN $version ($goos/$goarch) into $INSTALL_DIR"
curl -fsSL "$base/$asset" -o "$tmp/$asset" ||
  die "download failed: $base/$asset"
curl -fsSL "$base/$asset.sha256" -o "$tmp/$asset.sha256" ||
  die "download failed: $base/$asset.sha256"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp" && sha256sum -c "$asset.sha256")
elif command -v shasum >/dev/null 2>&1; then
  (cd "$tmp" && shasum -a 256 -c "$asset.sha256")
else
  die "sha256sum or shasum is required to verify the download"
fi

tar -xzf "$tmp/$asset" -C "$tmp"

if [ "$SYSTEM" = "1" ]; then
  # System service install: the freshly extracted binary installs itself to
  # the standard system layout via its own lifecycle command.
  [ "$goos" = linux ] || die "system service install requires Linux (per-user mode also supports macOS)"
  [ -d /run/systemd/system ] || die "system service install requires a running systemd"
  [ "$(id -u)" = 0 ] || die "system service install must run as root: sudo OPENTAMBO_SYSTEM=1 sh"
  # shellcheck disable=SC2086
  exec "$tmp/$BIN" install ${OPENTAMBO_INSTALL_ARGS:-}
fi

mkdir -p "$INSTALL_DIR"
if ! install -m 755 "$tmp/$BIN" "$INSTALL_DIR/$BIN" 2>/dev/null; then
  # Overwriting a running binary fails on some systems; replace via temp file.
  rm -f "$INSTALL_DIR/$BIN"
  cp "$tmp/$BIN" "$INSTALL_DIR/$BIN"
  chmod 755 "$INSTALL_DIR/$BIN"
fi

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "note: $INSTALL_DIR is not in PATH; add it with:"
     say "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
say "done: $INSTALL_DIR/$BIN $("$INSTALL_DIR/$BIN" --version 2>/dev/null || echo "$version")"
