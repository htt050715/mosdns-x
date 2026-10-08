# 192.168.50.110 部署完成

面板：**http://192.168.50.110:9099/**

DNS：`192.168.50.110:53`，UDP / TCP。已替换现有 `/etc/mosdns/mosdns` 并沿用 `mosdns.service`，开机自动启动。主配置 `/etc/mosdns/config.yaml` 保留原有内容，仅追加 API 设置；ECS、学校域名、Tailscale、广告拦截和原上游路径继续使用。AdGuard Home 的 Docker 容器及 10053 端口保持原运行状态。

面板保留最近 3000 条查询日志。配置写入当前关闭（`allow_config_write: false`）；可查看运行配置和统计。面板只绑定本机 LAN 地址，防火墙只允许 `192.168.50.0/24` 从 `enp6s19` 访问 TCP 9099。未开放互联网访问。

## 一键重复部署

机器上的文件已放在 `/root/mosdns-webui-deploy`。SSH 登录该机器后执行：

```sh
sh /root/mosdns-webui-deploy/deploy-mosdns-webui.sh
```

脚本默认选用同目录 `mosdns-x-webui-linux-amd64-production`，内置对应 SHA-256 校验；先测试现有 DNS，再在 15453 / 19099 的回环临时端口测试新版，成功后创建备份并短暂重启 `mosdns.service`。上线后继续检查 UDP/TCP、国内外解析和 API；切换后失败会恢复备份。

部署包：`mosdns-x-webui-deploy-192.168.50.110.tar.gz`。包内包含二进制、部署脚本、防火墙脚本、此说明及许可证，不包含 SSH 密码或机器上的用户配置。将包上传机器，解压后运行：

```sh
tar -xzf mosdns-x-webui-deploy-192.168.50.110.tar.gz
cd mosdns-webui-deploy
sudo sh deploy-mosdns-webui.sh
```

该脚本针对已检查过的 Debian/systemd 安装路径 `/etc/mosdns`，要求 `python3`、`curl`、`dig`、`sha256sum`、`ss`、`systemctl` 和 `iptables`。不要直接用于 OpenWrt、ARM 或不同安装布局；脚本遇到不同布局会退出。

正式部署二进制的版本标识为 `4.6.0`，构建日期为 `26.10.08`。保留正式版本标识是因为现有 `mosdns.apad.pro/api-query` 会拒绝带 `webui-preview` 后缀的 User-Agent；面板功能仍是本次移植。生产二进制 SHA-256：

```text
273902953ef4b948ad609dc639e2403e373a88b1cc526637d10e20b112a0795f
```

## 回滚到本次部署前

本次原始程序、配置、规则和服务文件的备份保存在：

```text
/var/backups/mosdns-webui/20261008-181400.CeeteE
```

恢复原程序和主配置，并移除本次面板防火墙规则：

```sh
sh /var/backups/mosdns-webui/20261008-181400.CeeteE/rollback.sh
```

本地也提供 `rollback-192.168.50.110.sh`。回滚不会覆盖规则文件，也不会改动 AdGuard Home 容器；主配置会恢复到备份时内容。重复部署会产生新的备份目录，脚本会打印对应回滚命令。最近备份路径记录在 `/etc/mosdns/webui-last-backup.txt`。

## 已验证

- 原服务、临时测试实例及上线服务均通过 `baidu.com` / `github.com` 的 UDP/TCP A 查询。
- 从用户的 Windows LAN 电脑直接查询 `192.168.50.110:53` 成功。
- LAN 访问面板、系统状态、缓存统计、查询统计 API 成功。
- 浏览器可查看真实客户端查询日志和概览；截图 `deployed-192.168.50.110.png`。
- 原主配置内容和规则文本与备份一致；仅追加面板 API 设置。
- `mosdns.service` 与 `mosdns-webui-firewall.service` 均 active/enabled；AdGuard Home 仍运行。
- 生产切换前曾检测到预览版本被上游拒绝，脚本正确终止，未替换原服务；正式版本构建通过同样检查后才上线。
- 部署、回滚及防火墙脚本通过远程 `sh -n` 校验。回滚脚本未在正在使用的生产 DNS 上执行测试。

机器时钟与客户端显示略有差异；本次任务没有调整系统时间，也没有重启整机。开机自启通过 systemd 的 enabled 状态确认。
