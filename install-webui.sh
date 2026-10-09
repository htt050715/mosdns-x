#!/bin/sh
# Download a versioned release and upgrade the running native mosdns service.
set -eu
umask 077
REPO=${MOSDNS_REPO:-htt050715/mosdns-x}
VERSION=${MOSDNS_WEBUI_VERSION:-webui-v2026.10.09}
[ "$(id -u)" = 0 ] || { echo 'Run with sudo sh install-webui.sh' >&2; exit 1; }
[ "$(uname -s)" = Linux ] || { echo 'Requires Linux/systemd.' >&2; exit 1; }
for tool in curl python3 systemctl tar sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "Missing $tool; install it first." >&2; exit 1; }
done
case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Supported architectures: amd64 / arm64.' >&2; exit 1;; esac
case "$REPO" in *[!A-Za-z0-9_./-]*|/*|*..*) echo 'Invalid MOSDNS_REPO' >&2; exit 1;; esac
case "$VERSION" in *[!A-Za-z0-9_.-]*|''|*..*) echo 'Invalid release version' >&2; exit 1;; esac
TMP=$(mktemp -d /tmp/mosdns-webui-install.XXXXXX)
cleanup() {
  rm -f "$TMP/detect-install.py" "$TMP/package.tar.gz" "$TMP/SHA256SUMS" "$TMP/package.sha256"
  if [ -d "$TMP/mosdns-webui-deploy" ]; then
    for name in mosdns-x-webui-linux-amd64-production mosdns-x-webui-linux-arm64-production mosdns-x-webui-linux-amd64-production.sha256 mosdns-x-webui-linux-arm64-production.sha256 deploy-mosdns-webui.sh detect-install.py panel-apply.sh LICENSE; do rm -f "$TMP/mosdns-webui-deploy/$name"; done
    rmdir "$TMP/mosdns-webui-deploy" 2>/dev/null || true
  fi
  rmdir "$TMP" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
download() {
  # HTTP/1.1 avoids incomplete HTTP/2 streams on some router/proxy paths.
  curl --http1.1 --fail --location --silent --show-error --retry 3 --retry-all-errors \
    --retry-max-time 300 --connect-timeout 15 --max-time 180 --proto '=https' --tlsv1.2 "$1" -o "$2"
}
download "https://raw.githubusercontent.com/$REPO/$VERSION/scripts/deploy/detect-install.py" "$TMP/detect-install.py"
detected=$(python3 "$TMP/detect-install.py")
eval "$detected"
printf 'Detected service: %s\nBinary: %s\nConfig: %s\nWorking directory: %s\nPanel: http://%s:%s/\n' "$MOSDNS_SERVICE" "$MOSDNS_BINARY" "$MOSDNS_CONFIG" "$MOSDNS_ROOT" "$PANEL_IP" "$PANEL_PORT"
if [ "${1:-}" = --detect-only ]; then exit 0; fi
asset=mosdns-x-webui-linux-$arch.tar.gz
url=https://github.com/$REPO/releases/download/$VERSION
download "$url/$asset" "$TMP/package.tar.gz"
download "$url/SHA256SUMS" "$TMP/SHA256SUMS"
hash=$(awk -v name="$asset" '$2 == name {print $1}' "$TMP/SHA256SUMS")
[ "${#hash}" = 64 ] || { echo 'Missing package SHA-256.' >&2; exit 1; }
printf '%s  %s\n' "$hash" "$TMP/package.tar.gz" > "$TMP/package.sha256"
sha256sum -c "$TMP/package.sha256"
tar -xzf "$TMP/package.tar.gz" -C "$TMP"
MOSDNS_DETECTED=1 sh "$TMP/mosdns-webui-deploy/deploy-mosdns-webui.sh" "$TMP/mosdns-webui-deploy/mosdns-x-webui-linux-$arch-production"
