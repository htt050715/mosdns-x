#!/bin/sh
# Download a versioned release and upgrade the running native mosdns service.
set -eu
umask 077
REPO=${MOSDNS_REPO:-htt050715/mosdns-x}
VERSION=${MOSDNS_WEBUI_VERSION:-webui-v2026.10.09}
GITHUB_PROXY=${MOSDNS_GITHUB_PROXY-https://ghproxy.05160715.xyz}
GITHUB_PROXY=${GITHUB_PROXY%/}
[ "$(id -u)" = 0 ] || { echo 'Run with sudo sh install-webui.sh' >&2; exit 1; }
[ "$(uname -s)" = Linux ] || { echo 'Requires Linux/systemd.' >&2; exit 1; }
for tool in curl python3 systemctl tar sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { echo "Missing $tool; install it first." >&2; exit 1; }
done
case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Supported architectures: amd64 / arm64.' >&2; exit 1;; esac
case "$REPO" in *[!A-Za-z0-9_./-]*|/*|*..*) echo 'Invalid MOSDNS_REPO' >&2; exit 1;; esac
case "$VERSION" in *[!A-Za-z0-9_.-]*|''|*..*) echo 'Invalid release version' >&2; exit 1;; esac
python3 - "$GITHUB_PROXY" <<'PY'
import sys
from urllib.parse import urlsplit
if sys.argv[1]:
    url=urlsplit(sys.argv[1])
    if url.scheme != 'https' or not url.hostname or url.username or url.password or url.query or url.fragment:
        raise SystemExit('MOSDNS_GITHUB_PROXY must be an HTTPS URL without credentials, query or fragment; use an empty value to disable.')
PY
TMP=$(mktemp -d /tmp/mosdns-webui-install.XXXXXX)
cleanup() {
  rm -f "$TMP/detect-install.py" "$TMP/package.tar.gz" "$TMP/SHA256SUMS" "$TMP/package.sha256" "$TMP/release.json"
  if [ -d "$TMP/mosdns-webui-deploy" ]; then
    for name in mosdns-x-webui-linux-amd64-production mosdns-x-webui-linux-arm64-production mosdns-x-webui-linux-amd64-production.sha256 mosdns-x-webui-linux-arm64-production.sha256 deploy-mosdns-webui.sh detect-install.py panel-apply.sh LICENSE; do rm -f "$TMP/mosdns-webui-deploy/$name"; done
    rmdir "$TMP/mosdns-webui-deploy" 2>/dev/null || true
  fi
  rmdir "$TMP" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
download_proxy() {
  proxy_source=$1
  proxy_destination=$2
  shift 2
  [ -n "$GITHUB_PROXY" ] || return 1
  # ghproxy supports GitHub files and raw source; keep API requests direct.
  case "$proxy_source" in https://github.com/*|https://raw.githubusercontent.com/*) ;; *) return 1;; esac
  printf 'GitHub acceleration: %s\n' "$GITHUB_PROXY" >&2
  if curl --http1.1 --fail --location --silent --show-error --retry 1 --retry-all-errors \
      --retry-max-time 90 --connect-timeout 10 --max-time 120 --proto '=https' --tlsv1.2 \
      "$@" "$GITHUB_PROXY/$proxy_source" -o "$proxy_destination"; then
    return 0
  fi
  echo 'GitHub acceleration failed; falling back to the official download.' >&2
  return 1
}
download() {
  # HTTP/1.1 avoids incomplete HTTP/2 streams on some router/proxy paths.
  download_source=$1
  download_destination=$2
  shift 2
  if download_proxy "$download_source" "$download_destination" "$@"; then return 0; fi
  curl --http1.1 --fail --location --silent --show-error --retry 3 --retry-all-errors \
    --retry-max-time 300 --connect-timeout 15 --max-time 180 --proto '=https' --tlsv1.2 "$@" "$download_source" -o "$download_destination"
}
download "https://raw.githubusercontent.com/$REPO/$VERSION/scripts/deploy/detect-install.py" "$TMP/detect-install.py"
detected=$(python3 "$TMP/detect-install.py")
eval "$detected"
printf 'Detected service: %s\nBinary: %s\nConfig: %s\nWorking directory: %s\nPanel: http://%s:%s/\n' "$MOSDNS_SERVICE" "$MOSDNS_BINARY" "$MOSDNS_CONFIG" "$MOSDNS_ROOT" "$PANEL_IP" "$PANEL_PORT"
if [ "${1:-}" = --detect-only ]; then exit 0; fi
asset=mosdns-x-webui-linux-$arch.tar.gz
url=https://github.com/$REPO/releases/download/$VERSION
download_release() {
  name=$1
  destination=$2
  if download_proxy "$url/$name" "$destination" --range 0-; then return 0; fi
  if curl --http1.1 --fail --location --silent --show-error --range 0- \
      --connect-timeout 15 --max-time 90 --proto '=https' --tlsv1.2 "$url/$name" -o "$destination"; then
    return 0
  fi
  echo "Release download interrupted; retrying via the official GitHub API: $name" >&2
  if [ ! -f "$TMP/release.json" ]; then
    download "https://api.github.com/repos/$REPO/releases/tags/$VERSION" "$TMP/release.json"
  fi
  api_asset=$(python3 - "$TMP/release.json" "$name" "$REPO" <<'PY'
import json, sys
with open(sys.argv[1]) as f: release=json.load(f)
matches = [a for a in release['assets'] if a['name'] == sys.argv[2] and a['state'] == 'uploaded']
if len(matches) != 1: raise SystemExit('Release asset missing or ambiguous')
print('https://api.github.com/repos/'+sys.argv[3]+'/releases/assets/'+str(int(matches[0]['id'])))
PY
  )
  download "$api_asset" "$destination" -H 'Accept: application/octet-stream' --range 0-
}
download_release "$asset" "$TMP/package.tar.gz"
download_release SHA256SUMS "$TMP/SHA256SUMS"
hash=$(awk -v name="$asset" '$2 == name {print $1}' "$TMP/SHA256SUMS")
[ "${#hash}" = 64 ] || { echo 'Missing package SHA-256.' >&2; exit 1; }
printf '%s  %s\n' "$hash" "$TMP/package.tar.gz" > "$TMP/package.sha256"
sha256sum -c "$TMP/package.sha256"
tar -xzf "$TMP/package.tar.gz" -C "$TMP"
MOSDNS_DETECTED=1 sh "$TMP/mosdns-webui-deploy/deploy-mosdns-webui.sh" "$TMP/mosdns-webui-deploy/mosdns-x-webui-linux-$arch-production"
