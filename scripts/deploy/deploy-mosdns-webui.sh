#!/bin/sh
# Deploy the dashboard binary onto the existing Debian/systemd mosdns-x install.
# Preserves DNS policy; verifies temporary listeners before the brief service cutover.
set -eu
umask 077
SERVICE=mosdns
ROOT=/etc/mosdns
BINARY=$ROOT/mosdns
CONFIG=$ROOT/config.yaml
PANEL_IP=${PANEL_IP:-192.168.50.110}
PANEL_PORT=${PANEL_PORT:-9099}
PREFLIGHT_PORT=${PREFLIGHT_PORT:-15453}
PREFLIGHT_API_PORT=${PREFLIGHT_API_PORT:-19099}
ENABLE_CONFIG_WRITE=${ENABLE_CONFIG_WRITE:-false}
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PAYLOAD=${1:-$SCRIPT_DIR/mosdns-x-webui-linux-amd64-production}
EXPECTED_SHA256=${2:-273902953ef4b948ad609dc639e2403e373a88b1cc526637d10e20b112a0795f}
BACKUP=
STAGE=
TEST_PID=
CUTOVER=0
COMMITTED=0

say() { printf '%s\n' "$*"; }
fail() { say "ERROR: $*" >&2; exit 1; }
cleanup() {
  rc=$?
  trap - EXIT HUP INT TERM
  if [ -n "$TEST_PID" ]; then kill "$TEST_PID" 2>/dev/null || true; wait "$TEST_PID" 2>/dev/null || true; fi
  if [ "$CUTOVER" = 1 ] && [ "$COMMITTED" = 0 ]; then
    say "Deployment failed. Restoring previous binary/config from $BACKUP"
    sh "$BACKUP/rollback.sh" || say "AUTOMATIC ROLLBACK FAILED: run sh $BACKUP/rollback.sh" >&2
  fi
  if [ -n "$STAGE" ] && [ -d "$STAGE" ]; then
    # These are known files in the installer-created staging directory only.
    rm -f "$STAGE/mosdns" "$STAGE/config.yaml" "$STAGE/preflight.yaml" "$STAGE/rollback.sh" "$STAGE/preflight.log" "$STAGE/expected.txt" "$STAGE/dns-result.txt" "$STAGE/health.json"
    rmdir "$STAGE" 2>/dev/null || true
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

[ "$(id -u)" = 0 ] || fail 'Run as root (sudo sh deploy-mosdns-webui.sh ...).'
[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || fail 'This payload is for Linux x86_64.'
for tool in python3 curl dig sha256sum systemctl ss cp mktemp; do command -v "$tool" >/dev/null 2>&1 || fail "Required command missing: $tool"; done
[ -f "$PAYLOAD" ] || fail "Binary not found: $PAYLOAD"
[ -f "$BINARY" ] && [ -f "$CONFIG" ] || fail 'Expected existing /etc/mosdns/mosdns and config.yaml.'
[ ! -L "$BINARY" ] && [ ! -L "$CONFIG" ] || fail 'Symlink installs require a tailored deployment.'
[ -n "$EXPECTED_SHA256" ] || fail 'Supply the SHA-256 from the deployment package.'
case "$ENABLE_CONFIG_WRITE" in true|false) ;; *) fail 'ENABLE_CONFIG_WRITE must be true or false.';; esac
python3 - "$PANEL_IP" "$PANEL_PORT" "$PREFLIGHT_PORT" "$PREFLIGHT_API_PORT" <<'PY'
import ipaddress, sys
ipaddress.ip_address(sys.argv[1])
assert ipaddress.ip_address(sys.argv[1]).version == 4
ports = [int(p) for p in sys.argv[2:]]
assert all(1024 <= p <= 65535 for p in ports)
assert len(set(ports)) == 3
PY
systemctl is-active --quiet "$SERVICE" || fail 'Existing mosdns service must be running before deployment.'
systemctl show "$SERVICE" --property=ExecStart --value | grep -F '/etc/mosdns/mosdns start --as-service -d /etc/mosdns' >/dev/null || fail 'Service command differs from this machine; inspect before deploying.'
[ "$(systemctl show "$SERVICE" --property=FragmentPath --value)" = /etc/systemd/system/mosdns.service ] || fail 'Unexpected service unit.'
for port in "$PREFLIGHT_PORT" "$PREFLIGHT_API_PORT"; do
  [ -z "$(ss -H -lntu "sport = :$port")" ] || fail "Preflight port $port is already occupied."
done
if [ -n "$(ss -H -lnt "sport = :$PANEL_PORT")" ]; then
  # Repeat deployments may reuse the API listener owned by the existing service.
  mainpid=$(systemctl show "$SERVICE" --property=MainPID --value)
  ss -H -lntp "sport = :$PANEL_PORT" | grep -F "pid=$mainpid," >/dev/null || fail "Panel port $PANEL_PORT belongs to another process."
fi

STAGE=$(mktemp -d "$ROOT/.webui-deploy.XXXXXX")
printf '%s  %s\n' "$EXPECTED_SHA256" "$PAYLOAD" > "$STAGE/expected.txt"
sha256sum -c "$STAGE/expected.txt"
cp "$PAYLOAD" "$STAGE/mosdns"
chmod 0755 "$STAGE/mosdns"
"$STAGE/mosdns" version

say 'Preparing candidate config; original rules/upstreams/ports remain in place.'
python3 - "$CONFIG" "$STAGE/config.yaml" "$STAGE/preflight.yaml" "$PANEL_IP" "$PANEL_PORT" "$PREFLIGHT_PORT" "$PREFLIGHT_API_PORT" "$ENABLE_CONFIG_WRITE" <<'PY'
import pathlib, re, sys
source, candidate, preflight, address, port, dns_port, api_port, writable = sys.argv[1:]
original = pathlib.Path(source).read_text(encoding='utf-8')
if re.search(r'^include\s*:', original, re.M):
    raise SystemExit('This installer expects servers in the main YAML file; include requires tailored preflight.')
if re.search(r'^(?:---|\.\.\.)\s*$', original, re.M):
    raise SystemExit('Multiple/explicit YAML documents require tailored migration.')
lines = original.splitlines(keepends=True)
out, skipping = [], False
for line in lines:
    if re.match(r'^api\s*:', line):
        if not re.match(r'^api\s*:\s*(?:#.*)?$', line.rstrip('\r\n')):
            raise SystemExit('Inline API config requires tailored migration.')
        skipping = True
        continue
    if skipping and re.match(r'^[A-Za-z_][\w-]*\s*:', line):
        skipping = False
    if not skipping:
        out.append(line)
body = ''.join(out).rstrip()+'\n'
api = '\napi:\n  http: "'+address+':'+port+'"\n  webui: true\n  audit_capacity: 3000\n  allow_config_write: '+writable+'\n'
pathlib.Path(candidate).write_text(body+api, encoding='utf-8')
# Match only the two known main server listener addresses. No global string replacement.
testbody, count = re.subn(r'(^\s+addr:\s*[\"\x27]?)0\.0\.0\.0:53([\"\x27]?\s*(?:#.*)?$)',
                         lambda m: m[1]+'127.0.0.1:'+dns_port+m[2], body, flags=re.M)
if count != 2:
    raise SystemExit('Expected exactly two UDP/TCP listeners on 0.0.0.0:53; tailor preflight to your config.')
testapi = '\napi:\n  http: "127.0.0.1:'+api_port+'"\n  webui: true\n  audit_capacity: 100\n  allow_config_write: false\n'
pathlib.Path(preflight).write_text(testbody+testapi, encoding='utf-8')
PY
chmod --reference="$CONFIG" "$STAGE/config.yaml"
chown --reference="$CONFIG" "$STAGE/config.yaml"

probe_dns() {
  address=$1
  port=$2
  for proto in udp tcp; do
    tcpflag=
    [ "$proto" = udp ] || tcpflag=+tcp
    for domain in baidu.com github.com; do
      dig "@$address" -p "$port" "$domain" A $tcpflag +time=8 +tries=1 +noall +comments +answer > "$STAGE/dns-result.txt" || return 1
      grep -F 'status: NOERROR' "$STAGE/dns-result.txt" >/dev/null || return 1
      grep -E '[[:space:]]IN[[:space:]]+A[[:space:]]' "$STAGE/dns-result.txt" >/dev/null || return 1
      say "DNS OK: $address:$port $proto $domain"
    done
  done
}
probe_api() {
  curl --fail --silent --max-time 3 --noproxy '*' "http://$1/api/v1/system/info" > "$STAGE/health.json" || return 1
  python3 - "$STAGE/health.json" <<'PY'
import json, sys
with open(sys.argv[1]) as f: data=json.load(f)
assert data['version'] in ('4.6.0-webui-preview', '4.6.0'), data
assert 'config_write' in data and data['platform'] == 'linux/amd64', data
PY
}

say 'Checking baseline DNS before deployment.'
probe_dns 127.0.0.1 53 || fail 'Existing DNS baseline failed; no changes made.'
say 'Starting preflight on loopback temporary ports.'
(cd "$ROOT"; exec "$STAGE/mosdns" start -d "$ROOT" -c "$STAGE/preflight.yaml") > "$STAGE/preflight.log" 2>&1 &
TEST_PID=$!
ready=0
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  kill -0 "$TEST_PID" 2>/dev/null || { cat "$STAGE/preflight.log"; fail 'Candidate process exited during preflight.'; }
  if probe_api "127.0.0.1:$PREFLIGHT_API_PORT"; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || { cat "$STAGE/preflight.log"; fail 'Preflight API did not become ready.'; }
probe_dns 127.0.0.1 "$PREFLIGHT_PORT" || { cat "$STAGE/preflight.log"; fail 'Preflight DNS checks failed.'; }
kill "$TEST_PID"
wait "$TEST_PID" 2>/dev/null || true
TEST_PID=

mkdir -p /var/backups/mosdns-webui
BACKUP=$(mktemp -d /var/backups/mosdns-webui/"$(date +%Y%m%d-%H%M%S)".XXXXXX)
cp -a "$ROOT" "$BACKUP/mosdns-original"
cp -a /etc/systemd/system/mosdns.service "$BACKUP/mosdns.service"
cat > "$BACKUP/rollback.sh" <<'SH'
#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
[ -f "$HERE/mosdns-original/mosdns" ] && [ -f "$HERE/mosdns-original/config.yaml" ]
systemctl stop mosdns
if [ -f "$HERE/firewall-created" ]; then
  systemctl disable --now mosdns-webui-firewall.service || true
  rm -f /etc/systemd/system/mosdns-webui-firewall.service /etc/mosdns-webui-firewall/allow-lan.sh
  rmdir /etc/mosdns-webui-firewall 2>/dev/null || true
  systemctl daemon-reload
fi
cp -a "$HERE/mosdns-original/mosdns" /etc/mosdns/.rollback-binary
cp -a "$HERE/mosdns-original/config.yaml" /etc/mosdns/.rollback-config
mv -f /etc/mosdns/.rollback-binary /etc/mosdns/mosdns
mv -f /etc/mosdns/.rollback-config /etc/mosdns/config.yaml
systemctl reset-failed mosdns
systemctl start mosdns
sleep 2
systemctl is-active --quiet mosdns
echo "Restored binary/config from $HERE. Rules and service unit were not changed by the installer."
SH
chmod 0700 "$BACKUP/rollback.sh"
cp "$STAGE/preflight.log" "$BACKUP/preflight.log"
cp "$STAGE/config.yaml" "$BACKUP/deployed-config.yaml"
say "Backup ready: $BACKUP"
say 'Switching existing mosdns service to the verified binary/config.'
CUTOVER=1
systemctl stop "$SERVICE"
mv -f "$STAGE/mosdns" "$BINARY"
mv -f "$STAGE/config.yaml" "$CONFIG"
systemctl reset-failed "$SERVICE"
systemctl start "$SERVICE"
ready=0
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  if systemctl is-active --quiet "$SERVICE" && probe_api "$PANEL_IP:$PANEL_PORT"; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || { journalctl -u "$SERVICE" -n 30 --no-pager; fail 'Production API readiness failed.'; }
probe_dns 127.0.0.1 53 || fail 'Production UDP/TCP DNS health check failed.'
curl --fail --silent --max-time 3 --noproxy '*' "http://$PANEL_IP:$PANEL_PORT/" >/dev/null || fail 'Dashboard page failed.'
sleep 2
systemctl is-active --quiet "$SERVICE" || fail 'Service exited after health checks.'
if [ -f "$SCRIPT_DIR/configure-webui-firewall.sh" ]; then
  [ -f /etc/systemd/system/mosdns-webui-firewall.service ] || touch "$BACKUP/firewall-created"
  PANEL_IP="$PANEL_IP" PANEL_PORT="$PANEL_PORT" sh "$SCRIPT_DIR/configure-webui-firewall.sh"
fi
COMMITTED=1
printf '%s\n' "$BACKUP" > "$ROOT/webui-last-backup.txt"
say 'Deployment complete.'
say "Panel: http://$PANEL_IP:$PANEL_PORT/"
say 'DNS: existing UDP/TCP port 53, original routing policy retained.'
say "Config editor enabled: $ENABLE_CONFIG_WRITE"
say "Rollback: sh $BACKUP/rollback.sh"
say "Autostart: $(systemctl is-enabled "$SERVICE" 2>/dev/null || true)"
