# 面板一键安装 / 升级

本仓库的面板直接嵌入 mosdns-x 二进制，无需独立启动 Node.js 服务。功能包括查询日志和返回 IP、YAML 格式化/校验/保存/应用、上游与上游组、远程域名规则集、规则内容编辑，以及域名集到上游/上游组的可视化路由。

## 已有 `/etc/mosdns` 安装

适用于 Linux amd64 / arm64、systemd 管理的原生 mosdns-x v4 服务，主配置为 YAML，二进制和配置均在工作目录内，DNS 使用 `0.0.0.0:53` 的 UDP/TCP 双监听。Debian / Ubuntu 安装依赖：

```sh
sudo apt-get update && sudo apt-get install -y curl python3 dnsutils iproute2
```

一条命令下载并安装（root 登录时去掉 `sudo`）：

```sh
curl -fsSL https://ghproxy.05160715.xyz/https://raw.githubusercontent.com/htt050715/mosdns-x/main/install-webui.sh -o /tmp/install-mosdns-webui.sh && sudo sh /tmp/install-mosdns-webui.sh
```

安装器读取正在运行的 systemd 服务和 `/proc` 启动参数，自动识别实际二进制、工作目录和配置。标准 `/etc/mosdns/mosdns start --as-service -d /etc/mosdns` 会识别为 `/etc/mosdns/mosdns` 与 `/etc/mosdns/config.yaml`。

已有面板时保留其明确的 IPv4 地址及端口；首次安装默认 `127.0.0.1:9099`。需要在局域网访问时指定本机局域网 IP：

```sh
sudo env PANEL_IP=192.168.50.110 PANEL_PORT=9099 sh /tmp/install-mosdns-webui.sh
```

面板提供配置写入和服务重启能力，只应在可信局域网或经认证的反向代理下使用。安装器不会修改防火墙；现有防火墙策略继续保留。

## 先检测，不改服务

安装入口、检测脚本、Release 二进制包和 `SHA256SUMS` 默认通过 `https://ghproxy.05160715.xyz/原始GitHub链接` 加速下载。脚本内加速下载失败时依次回退官方下载地址与官方资产 API，校验流程保持一致，API 请求使用官方地址。

可用 `MOSDNS_GITHUB_PROXY` 更换 HTTPS 加速前缀，或设为空关闭加速（安装入口本身也可直接从 `https://raw.githubusercontent.com/htt050715/mosdns-x/main/install-webui.sh` 下载）：

```sh
sudo env MOSDNS_GITHUB_PROXY="" sh /tmp/install-mosdns-webui.sh
```

```sh
sudo sh /tmp/install-mosdns-webui.sh --detect-only
```

存在多个服务时必须明确指定，例如：

```sh
sudo env MOSDNS_SERVICE=mosdns.service sh /tmp/install-mosdns-webui.sh
```

可覆盖 `MOSDNS_ROOT`、`MOSDNS_BINARY`、`MOSDNS_CONFIG`，但路径必须与实际运行服务匹配，且不是符号链接。无法可靠识别时停止安装。Docker、OpenWrt/procd、mosdns v5、include 配置和非标准监听需要单独迁移，不会被此脚本直接覆盖。

## 安装过程与回滚

1. 下载固定版本 Release 包，校验包和二进制的 SHA-256。
2. 保持既有规则、上游、DNS 监听和 systemd 启动参数，新增/更新 `api` 面板段。
3. 检查现有 DNS，然后用临时 loopback 端口运行新二进制，验证面板 API 和 UDP/TCP DNS。
4. 完整备份工作目录至 `/var/backups/mosdns-webui/` 后，短暂停止服务并替换二进制和配置。
5. 启动后复查 DNS/面板，失败自动恢复；成功时打印备份路径和手动回滚命令。

最近一次备份路径记录在工作目录的 `webui-last-backup.txt`。手动回滚示例：

```sh
sudo sh /var/backups/mosdns-webui/安装时打印的目录/rollback.sh
```

后续面板内「保存并应用」会校验配置，并通过独立 systemd 任务重启服务；健康检查失败自动恢复上一份生效配置。

## 发布与自行构建

推送 `webui-v*` 标签触发 GitHub Actions，先测试，再构建 Vue 与 Linux amd64/arm64 安装包。产物为 `mosdns-x-webui-linux-{amd64,arm64}.tar.gz` 与 `SHA256SUMS`。安装版本由 `install-webui.sh` 的 `VERSION` 固定，也可设置 `MOSDNS_WEBUI_VERSION` 选择已存在的标签。

本地构建需要 Go 1.26、Node.js 22、Python 3.9+：

```sh
python3 scripts/build-webui-release.py
```

面板管理的详细使用方式见 [管理功能说明](webui-management.md)。
