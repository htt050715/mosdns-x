# mosdns-x 面板移植

> 当前已更新为 management-v2，支持可视化上游分组、远程域名集、流量导向、规则编辑、响应 IP/TTL 与配置应用。请优先阅读 [最新管理说明](webui-management.md)。以下记录第一阶段移植时的范围与背景。

这是可运行的第一阶段移植：保留 mosdns-x 的解析内核和配置格式，将 jasonxtt/mosdns 的 Vue 界面组件接入 mosdns-x 的管理 API。并非 jasonxtt 分支的完整功能合并。

## 使用

在已有主配置中加入：

```yaml
api:
  http: 127.0.0.1:9099
  webui: true
  audit_capacity: 1000
  allow_config_write: false
```

重启后访问 `http://127.0.0.1:9099/`。现有 `/metrics`、`/plugins/` 和 pprof 接口继续使用同一个端口。`webui` 默认关闭，因此旧配置无需调整。启用面板必须设置 `api.http`。

需要浏览器编辑主配置时，手动设置 `allow_config_write: true` 并重启。编辑器支持 YAML / JSON / TOML 等当前配置加载器支持的格式，保留 `include` 引用，只编辑主文件。保存前校验语法、插件参数、重复标签和服务器入口，并使用 SHA-256 检查磁盘文件是否发生变化。保存会创建同目录备份，返回 `restart_required: true`；运行中的解析配置保持原状态，需自行重启服务。

运行资源（例如上游连通性、端口占用、证书、外部规则文件以及流水线内部引用）仍由启动流程检查。这里的基础校验不代替一次完整启动测试。

面板复用现有无认证管理端口。默认示例只绑定回环地址；跨设备使用应通过带认证的反向代理或 SSH 转发访问，尤其在开启配置编辑时。API 拒绝跨 Origin 浏览器访问，修改请求必须使用 JSON；这不等同于用户认证。配置编辑页面会显示完整主配置；其他运行配置视图对密码、密钥和 URL 用户信息做脱敏。

可直接尝试 `examples/webui.yaml`：

```sh
mosdns start -c examples/webui.yaml
# DNS: 127.0.0.1:15353 (UDP/TCP)
# WebUI: http://127.0.0.1:9099/
```

## 功能范围

| 面板功能 | 当前实现 |
|---|---|
| 概览和趋势 | 复用原面板 DnsOverviewCard / ECharts；显示累计请求、平均耗时与采样趋势 |
| 查询日志 | 在服务器入口记录最终响应；分页、搜索、详情、暂停/恢复、清空 |
| 运行状态 | 版本、平台、运行时间、Go 内存、goroutines |
| 上游设置 | 查看当前进程的 fast_forward 参数；通过主配置编辑修改，重启生效 |
| 规则管理 | 查看 data_providers、matcher 和 sequence；主配置编辑修改，重启生效 |
| 缓存统计 | 直接读取现有缓存插件 Prometheus 指标，含预置缓存 |
| 配置管理 | 可选启用；基础校验、备份、并发编辑检查 |
| 浅色/深色 | 本地浏览器保存主题 |

查询日志使用固定容量的内存环形队列，默认 1000 条，上限 50000 条；重启后清空。累计请求和耗时不受日志容量、暂停或清空影响，进程重启才重置。时间段统计来自保留日志，始终标记 `complete: false`，不能用于完整历史分析。趋势每 3 秒取累计统计差值，避免高流量时被日志采样上限截断。

日志中的 `entry` 是服务器入口流水线，当前没有声称它是实际命中的规则或胜出上游；一个请求可能在分支、并行上游和缓存中经过多条路径。内部插件失败导致 SERVFAIL 时，日志会记录实际最终 DNS 响应码。

尚未移植 jasonxtt 分支的 AliAPI、AdGuard 订阅管理、special_groups、SRS / sing-box 规则处理、自定义 switcher、自动生成路由配置、上游热重载、诊断抓取、更新二进制与配置包功能。它们依赖 v5 风格插件、查询上下文及其专用配置包，不能直接复用到本仓库的 v4 风格流水线。后续应逐项移植插件及真实运行行为，再开放对应 UI。

单条日志最多保存 64 条 Answer 记录，单条记录文本最多约 2 KiB，总 Answer 文本最多约 8 KiB；过长时标记 `answers_truncated`。日志展示限制不会修改返回给客户端的 DNS 响应。

## 构建

仓库包含已构建的嵌入资源，普通 `go build` 即可生成带面板的单文件程序。修改前端后必须重建：

```sh
cd webui
npm ci
npm run build
cd ..
go test ./coremain
go build -o mosdns .
```

需要 Node.js 20.19+ 或 22.12+，以及 `go.mod` 指定的 Go 版本。`release.py` 和 CI 会在 Go 编译前构建前端，避免发布旧面板资源。Go-only 开发无需重复下载前端依赖。

前端开发：

```sh
cd webui
MOSDNS_DEV_TARGET=http://127.0.0.1:9099 npm run dev
```

Vite 代理 `/api`、`/plugins` 和 `/metrics`。Windows PowerShell 可先执行 `$env:MOSDNS_DEV_TARGET='http://127.0.0.1:9099'`。

## 来源和许可证

- mosdns-x 基线：`htt050715/mosdns-x`，提交 `10d52d14255b97f73ff90e5d68705f240b20d071`。
- 面板来源：`jasonxtt/mosdns`，提交 `75f2667703c83952e86833852440e07c63f33311`。
- 复用 `webui-log` 的全局样式、Vue / ECharts 依赖、趋势卡片、确认气泡、格式化工具及相关服务类型。`webui/src/App.vue` 和管理 API 为本次适配新增；实时统计 composable 改为使用累计计数/耗时差值。
- 两个项目均采用 GNU GPL v3，本移植继续遵循仓库根目录的 `LICENSE`。复制部分的原作者权利和许可证继续保留。

## API

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/v1/system/info` | 系统状态和功能开关 |
| GET | `/api/v1/runtime/config` | 当前运行配置（脱敏、已合并 include） |
| GET | `/api/v1/cache/stats` | 缓存指标 |
| GET | `/api/v1/audit/status`、`/api/v1/audit/capacity` | 日志状态、容量 |
| POST | `/api/v1/audit/start`、`stop`、`clear` | 日志控制；请求体 `{}` |
| GET | `/api/v2/audit/stats` | 进程累计计数和耗时 |
| GET | `/api/v2/audit/stats/windows` | 保留日志时间段统计 |
| GET | `/api/v2/audit/logs?page=1&limit=50&q=example&exact=false` | 日志分页和搜索，支持多个 `client_ip` |
| GET | `/api/v1/config` | 主配置内容和 SHA-256；需启用编辑器 |
| POST | `/api/v1/config` | `{text, sha256, validate_only}`；需启用编辑器 |
