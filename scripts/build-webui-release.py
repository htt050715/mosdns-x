#!/usr/bin/env python3
"""Build embedded Vue assets and checksum-verified native Linux install packages."""
import argparse
from datetime import datetime, timezone
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def run(args, **kwargs):
    subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go', default='go')
    parser.add_argument('--output', default='release-webui')
    parser.add_argument('--skip-ui', action='store_true', help='Only when webui was freshly built')
    args = parser.parse_args()
    if not args.skip_ui:
        npm = shutil.which('npm.cmd' if os.name == 'nt' else 'npm')
        if not npm:
            parser.error('npm is required to build the embedded panel')
        run([npm, 'ci', '--prefix', 'webui', '--no-audit', '--no-fund'])
        run([npm, 'run', 'build', '--prefix', 'webui'])
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    checksums = []
    for arch in ('amd64', 'arm64'):
        with tempfile.TemporaryDirectory(prefix='mosdns-webui-build-') as temp:
            package = Path(temp) / 'mosdns-webui-deploy'
            package.mkdir()
            name = 'mosdns-x-webui-linux-' + arch + '-production'
            binary = package / name
            env = dict(os.environ, GOOS='linux', GOARCH=arch, CGO_ENABLED='0')
            buildtime = datetime.now(timezone.utc).strftime('%y.%m.%d')
            run([args.go, 'build', '-trimpath', '-ldflags=-s -w -X github.com/pmkol/mosdns-x/constant.Version=4.6.0 -X github.com/pmkol/mosdns-x/constant.BuildTime='+buildtime,
                 '-o', str(binary), '.'], env=env)
            binary.chmod(0o755)
            (package / (name+'.sha256')).write_text(hashlib.sha256(binary.read_bytes()).hexdigest()+'  '+name+'\n')
            for name in ('deploy-mosdns-webui.sh', 'detect-install.py', 'panel-apply.sh'):
                dest = package / name
                dest.write_bytes((ROOT / 'scripts' / 'deploy' / name).read_bytes())
                dest.chmod(0o755)
            shutil.copyfile(ROOT / 'LICENSE', package / 'LICENSE')
            asset = output / ('mosdns-x-webui-linux-'+arch+'.tar.gz')
            def permissions(info):
                info.mode = 0o755 if info.isdir() or info.name.endswith('-production') or info.name.endswith(('.sh', '.py')) else 0o644
                info.uid = info.gid = 0
                info.uname = info.gname = 'root'
                return info
            with tarfile.open(asset, 'w:gz') as archive:
                archive.add(package, arcname=package.name, filter=permissions)
            checksum = hashlib.sha256(asset.read_bytes()).hexdigest()
            checksums.append(checksum+'  '+asset.name+'\n')
            print('Built', asset.name, checksum, flush=True)
    (output / 'SHA256SUMS').write_text(''.join(checksums))


if __name__ == '__main__':
    main()
