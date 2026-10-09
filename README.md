## Mosdns-x

### WebUI 面板与一键安装

支持配置格式化/在线编辑与安全应用、上游分组、远程规则集、可视化域名分流、查询日志和返回 IP。完整安装说明见 [一键安装文档](docs/webui-install.md)，下载见 [本仓库 Releases](https://github.com/htt050715/mosdns-x/releases)。

已有 Linux/systemd 原生 mosdns-x（如 `/etc/mosdns`）可执行：

```sh
curl -fsSL https://ghproxy.05160715.xyz/https://raw.githubusercontent.com/htt050715/mosdns-x/main/install-webui.sh -o /tmp/install-mosdns-webui.sh && sudo sh /tmp/install-mosdns-webui.sh
```

自动检测当前服务和配置路径，保留已有规则与上游，先预检再备份升级，失败自动回滚。默认使用 `ghproxy.05160715.xyz` 加速源文件、Release 包和校验文件下载，失败回退官方地址；设置 `MOSDNS_GITHUB_PROXY=""` 可关闭加速。默认自动识别并监听本机局域网 IPv4 的 `9099` 端口，局域网设备可直接访问 `http://本机局域网IP:9099/`；已有明确的非回环 IPv4 地址和端口会保留。可用 `sudo env PANEL_IP=本机局域网IP sh /tmp/install-mosdns-webui.sh` 手动指定。root 用户可去掉 `sudo`。支持 amd64/arm64；Docker、OpenWrt/procd、v5 和 include 配置不适用。

本分支新增可选 Vue 管理面板：概览、查询日志、缓存统计、上游/规则查看及主配置编辑。启用方式和移植范围见 [面板说明](docs/webui-port.md)，可运行示例见 [examples/webui.yaml](examples/webui.yaml)。

Mosdns-x 是一个用 Go 编写的高性能 DNS 转发器，支持运行插件流水线，用户可以按需定制 DNS 处理逻辑。

**支持监听与请求以下类型的 DNS：**

* UDP
* TCP
* DNS over TLS - DoT
* DNS over QUIC - DoQ
* DNS over HTTP/2 - DoH
* DNS over HTTP/3 - DoH3

功能概述、配置方式、教程，详见：[wiki](https://github.com/pmkol/mosdns-x/wiki)

下载预编译文件、更新日志，详见：[release](https://github.com/pmkol/mosdns-x/releases)

#### 电报社区：

**[Mosdns-x Group](https://t.me/mosdns)**

#### 关联项目：

**[easymosdns](https://github.com/pmkol/easymosdns)**

适用于 Linux 的辅助脚本。借助 Mosdns-x，仅需几分钟即可搭建一台支持 ECS 的无污染 DNS 服务器。内置中国大陆地区的优化规则，满足DNS日常使用场景，开箱即用。

**[mosdns-v4](https://github.com/IrineSistiana/mosdns/tree/v4)**

一个插件化的 DNS 转发器。是 Mosdns-x 的上游项目。
