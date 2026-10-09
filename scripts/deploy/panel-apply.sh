#!/bin/sh
# Run in a separate transient systemd service so the DNS restart cannot kill this task.
set -eu
umask 077
CONFIG=$1
BACKUP=$2
STATUS=$3
ROOT=$(dirname -- "$CONFIG")
SERVICE=${MOSDNS_SERVICE:-mosdns}
DNS_PORT=${MOSDNS_DNS_PORT:-53}
GOOD=$CONFIG.panel-last-good
BINARY=${MOSDNS_BINARY:-$ROOT/mosdns}
API=$(python3 - "$CONFIG" <<'PY'
import pathlib,re,sys
s=pathlib.Path(sys.argv[1]).read_text()
m=re.search(r'^api:\s*\n(?:(?:[ \t]+.*|\s*)\n)*?\s+http:\s*["\x27]?([^\s"\x27#]+)',s,re.M)
if not m: raise SystemExit('API address missing')
print('http://'+m[1]+'/api/v1/system/info')
PY
)
report() {
  python3 - "$STATUS" "$1" "$2" <<'PY'
import json, pathlib, sys
from datetime import datetime, timezone
p=pathlib.Path(sys.argv[1])
try: value=json.loads(p.read_text())
except Exception: value={}
value.update(state=sys.argv[2], message=sys.argv[3], finished=datetime.now(timezone.utc).isoformat())
t=p.with_suffix('.tmp'); t.write_text(json.dumps(value)); t.replace(p)
PY
}
restore() {
  trap - EXIT HUP INT TERM
  [ -f "$GOOD" ] && source=$GOOD || source=$BACKUP
  cp -p "$source" "$CONFIG.panel-restore"
  mv -f "$CONFIG.panel-restore" "$CONFIG"
  systemctl reset-failed "$SERVICE"
  if systemctl restart "$SERVICE"; then
    sleep 2
    if systemctl is-active --quiet "$SERVICE" && curl -fsS --max-time 3 --noproxy '*' "$API" >/dev/null; then report rolled_back '新配置应用失败，已恢复上一份生效配置。'; else report failed '已恢复配置，但服务健康检查未通过，请查看 mosdns 服务日志。'; fi
  else report failed '恢复配置后服务仍未启动，请查看 mosdns 服务日志。'; fi
}
trap restore EXIT HUP INT TERM
sleep 2
"$BINARY" check -d "$ROOT" -c "$CONFIG" >"$ROOT/.panel-check.log" 2>&1
systemctl restart "$SERVICE"
ready=0
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if systemctl is-active --quiet "$SERVICE" && curl -fsS --max-time 2 --noproxy '*' "$API" >/dev/null; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ]
# Verify both wire transports; NXDOMAIN is a valid policy response, SERVFAIL is not.
for proto in udp tcp; do
  flag=; [ "$proto" = udp ] || flag=+tcp
  dig @127.0.0.1 -p "$DNS_PORT" baidu.com A $flag +time=5 +tries=2 +noall +comments | grep -E 'status: (NOERROR|NXDOMAIN)' >/dev/null
done
cp -p "$CONFIG" "$GOOD.tmp"
mv -f "$GOOD.tmp" "$GOOD"
report applied '配置已应用，DNS 与面板检查通过。'
trap - EXIT HUP INT TERM
