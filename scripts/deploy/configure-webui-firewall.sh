#!/bin/sh
# Dedicated LAN-only allowance; existing 1Panel and Tailscale policies stay intact.
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
PANEL_IP=${PANEL_IP:-192.168.50.110}
PANEL_PORT=${PANEL_PORT:-9099}
PANEL_SUBNET=${PANEL_SUBNET:-192.168.50.0/24}
INTERFACE=${PANEL_INTERFACE:-enp6s19}
python3 - "$PANEL_IP" "$PANEL_PORT" "$PANEL_SUBNET" "$INTERFACE" <<'PY'
import ipaddress, re, sys
ip=ipaddress.IPv4Address(sys.argv[1]); subnet=ipaddress.IPv4Network(sys.argv[3])
assert ip in subnet and 1024 <= int(sys.argv[2]) <= 65535
assert re.fullmatch(r'[A-Za-z0-9_.:-]+',sys.argv[4])
PY
ip link show "$INTERFACE" >/dev/null
IPTABLES=$(command -v iptables)
command -v systemctl >/dev/null
mkdir -p /etc/mosdns-webui-firewall
cat > /etc/mosdns-webui-firewall/allow-lan.sh <<SH
#!/bin/sh
set -eu
case "\${1:-start}" in
  start)
    "$IPTABLES" -w -C INPUT -i "$INTERFACE" -s "$PANEL_SUBNET" -d "$PANEL_IP" -p tcp --dport "$PANEL_PORT" -m comment --comment mosdns-webui-lan -j ACCEPT 2>/dev/null ||
    "$IPTABLES" -w -I INPUT 1 -i "$INTERFACE" -s "$PANEL_SUBNET" -d "$PANEL_IP" -p tcp --dport "$PANEL_PORT" -m comment --comment mosdns-webui-lan -j ACCEPT
    ;;
  stop)
    if "$IPTABLES" -w -C INPUT -i "$INTERFACE" -s "$PANEL_SUBNET" -d "$PANEL_IP" -p tcp --dport "$PANEL_PORT" -m comment --comment mosdns-webui-lan -j ACCEPT 2>/dev/null; then
      "$IPTABLES" -w -D INPUT -i "$INTERFACE" -s "$PANEL_SUBNET" -d "$PANEL_IP" -p tcp --dport "$PANEL_PORT" -m comment --comment mosdns-webui-lan -j ACCEPT
    fi
    ;;
  *) exit 2 ;;
esac
SH
chmod 0700 /etc/mosdns-webui-firewall/allow-lan.sh
cat > /etc/systemd/system/mosdns-webui-firewall.service <<'UNIT'
[Unit]
Description=Allow local LAN access to the MosDNS-X dashboard
After=network-online.target 1panel-agent.service 1panel-core.service
Wants=network-online.target
Before=mosdns.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh /etc/mosdns-webui-firewall/allow-lan.sh start
ExecStop=/bin/sh /etc/mosdns-webui-firewall/allow-lan.sh stop

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable mosdns-webui-firewall.service
sh /etc/mosdns-webui-firewall/allow-lan.sh start
systemctl start mosdns-webui-firewall.service
echo "Allowed $PANEL_SUBNET via $INTERFACE to $PANEL_IP:$PANEL_PORT, enabled at boot."
