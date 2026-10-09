#!/usr/bin/env python3
"""Detect a running systemd mosdns install, without executing config contents."""
import json
import ipaddress
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def lan_address():
    """Select an RFC1918 IPv4 on the default route, or one unique LAN address."""
    private = [ipaddress.ip_network(n) for n in ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16')]
    interfaces = json.loads(command('ip', '-j', '-4', 'addr', 'show', 'up'))
    candidates = []
    for interface in interfaces:
        name = interface['ifname']
        if name == 'lo' or name.startswith(('docker', 'veth', 'virbr', 'br-', 'tun', 'wg', 'tailscale')):
            continue
        for address in interface.get('addr_info', []):
            host = address.get('local', '')
            if address.get('family') == 'inet' and address.get('scope') == 'global' and any(ipaddress.ip_address(host) in net for net in private):
                candidates.append((name, host))
    try:
        routes = json.loads(command('ip', '-j', '-4', 'route', 'get', '1.1.1.1'))
    except (OSError, subprocess.CalledProcessError, ValueError):
        routes = []
    for route in routes:
        for name, host in candidates:
            if name == route.get('dev') and host == route.get('prefsrc'):
                return host
    hosts = sorted(set(host for _, host in candidates))
    if len(hosts) == 1:
        return hosts[0]
    raise ValueError('Cannot uniquely detect LAN IPv4; set PANEL_IP to the machine LAN address.')


def panel_address(config_text):
    api = re.search(r'^api:[ \t]*(?:#.*)?\n((?:[ \t]+[^\n]*\n|\n)*)', config_text + '\n', re.M)
    address = re.search(r'^\s+http:\s*["\x27]?([^\s"\x27#]+)', api[1]) if api else None
    host, port = None, '9099'
    if address and re.fullmatch(r'[0-9.]+:[0-9]+', address[1]):
        current, port = address[1].rsplit(':', 1)
        parsed = ipaddress.ip_address(current)
        # Migrate existing loopback/all-interface panel listeners to the LAN IP.
        if not parsed.is_loopback and not parsed.is_unspecified:
            host = current
    return (os.environ.get('PANEL_IP') or host or lan_address(), os.environ.get('PANEL_PORT') or port)


def parse_process(args, cwd):
    root, config = cwd, None
    for i, arg in enumerate(args):
        for flag in ('-d', '--dir'):
            if arg == flag and i + 1 < len(args):
                root = args[i + 1]
            elif arg.startswith(flag + '='):
                root = arg.split('=', 1)[1]
        for flag in ('-c', '--config'):
            if arg == flag and i + 1 < len(args):
                config = args[i + 1]
            elif arg.startswith(flag + '='):
                config = arg.split('=', 1)[1]
    root = Path(root)
    if not root.is_absolute():
        # /proc/PID/cwd already reflects mosdns's chdir(-d), not its launch cwd.
        root = Path(cwd)
    root = root.resolve()
    config = os.environ.get('MOSDNS_CONFIG') or config
    if config:
        config = Path(config)
        if not config.is_absolute():
            config = root / config
    else:
        candidates = [root / n for n in ('config.yaml', 'config.yml') if (root / n).is_file()]
        if len(candidates) != 1:
            raise ValueError('Cannot uniquely detect YAML config; set MOSDNS_CONFIG.')
        config = candidates[0]
    return str(root), str(config.absolute())


def detect():
    service = os.environ.get('MOSDNS_SERVICE')
    if not service:
        rows = command('systemctl', 'list-units', '--type=service', '--state=active', '--no-legend', '--plain').splitlines()
        candidates = [row.split()[0] for row in rows if row and re.fullmatch(r'mosdns[\w@.-]*\.service', row.split()[0]) and 'firewall' not in row.split()[0]]
        if len(candidates) != 1:
            raise ValueError('Expected one running mosdns systemd service; set MOSDNS_SERVICE explicitly.')
        service = candidates[0]
    if not re.fullmatch(r'[A-Za-z0-9_@.-]+', service):
        raise ValueError('Invalid service name')
    pid = int(command('systemctl', 'show', service, '--property=MainPID', '--value'))
    if pid <= 0 or command('systemctl', 'is-active', service) != 'active':
        raise ValueError('mosdns service must be running')
    process = Path('/proc') / str(pid)
    args = (process / 'cmdline').read_bytes().decode().rstrip('\0').split('\0')
    executable = os.readlink(process / 'exe')
    if executable.endswith(' (deleted)'):
        raise ValueError('Running executable was deleted; restart current service before installing')
    if 'start' not in args:
        raise ValueError('Service is not a native mosdns start process')
    root, config = parse_process(args, os.readlink(process / 'cwd'))
    values = {'MOSDNS_SERVICE': service, 'MOSDNS_ROOT': root, 'MOSDNS_BINARY': executable, 'MOSDNS_CONFIG': config}
    for key in ('MOSDNS_ROOT', 'MOSDNS_BINARY', 'MOSDNS_CONFIG'):
        values[key] = os.environ.get(key) or values[key]
    for key in ('MOSDNS_ROOT', 'MOSDNS_BINARY', 'MOSDNS_CONFIG'):
        path = Path(values[key])
        if not path.is_absolute() or not path.exists() or path.is_symlink():
            raise ValueError(f'{key} must be an existing absolute path without symlink: {path}')
    if Path(values['MOSDNS_CONFIG']).parent != Path(values['MOSDNS_ROOT']):
        raise ValueError('Config must be directly inside the working directory for this installer')
    if Path(values['MOSDNS_BINARY']).parent != Path(values['MOSDNS_ROOT']):
        raise ValueError('Binary must be directly inside the working directory for this installer')
    if os.path.realpath(values['MOSDNS_BINARY']) != executable:
        raise ValueError('Requested binary differs from the running service executable')
    config_text = Path(values['MOSDNS_CONFIG']).read_text(encoding='utf-8')
    values['PANEL_IP'], values['PANEL_PORT'] = panel_address(config_text)
    return values


if __name__ == '__main__':
    try:
        values = detect()
        if '--json' in sys.argv:
            print(json.dumps(values, indent=2))
        else:
            for key, value in values.items():
                print('export ' + key + '=' + shlex.quote(value))
    except (ValueError, OSError, subprocess.CalledProcessError) as e:
        sys.exit('Install detection failed: ' + str(e))
