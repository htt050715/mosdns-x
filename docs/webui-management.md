# Web 管理面板（management-v2）

原生 mosdns-x YAML 是配置来源；上游表单、域名匹配器、流量图和文本编辑共用一份草稿。支持保留注释的格式化、差异预览、SHA-256 并发编辑检查、备份、校验、保存和应用。当前可视化编辑针对 YAML 主文件；include 文件需要单独维护。旧版格式支持仍由加载器提供，但可视化管理请使用 YAML。

## 上游和分流

上游组对应原生 `fast_forward` 插件，可配置多个 UDP/TCP/DoH/DoT/DoQ 成员、bootstrap、代理和连接参数。`strategy` 可选 `parallel`（默认，保持原行为）、`fallback`（顺序故障转移）或 `round_robin`（轮换首选成员并故障转移）。后两种策略每个成员最长等待 2 秒，且受服务器总体查询超时约束。第一个成员按内核原行为视为可信。

规则管理可以编辑已配置的本地域名/IP/Hosts 文件（校验、备份并即时更新匹配器），新增/编辑远程域名集，编辑域名匹配器，展开流水线的 IF/ELSE 顺序及目标组。大文件每页 500 行，支持搜索；大于 2 MiB 的规则文件仅展示前 2 MiB，保持只读。

“添加分流”创建 `web_route_*` 域名匹配器，并在选定流水线位置插入 `if → 上游组 → _return`。默认放在缓存/ECS 处理前，早于它的 Hosts、学校和 Tailnet 分支保持更高优先级。选择位置时考虑缓存命中与 ECS 策略。复杂原有分支可以直接更换目标组或编辑条件；其余原生操作通过 YAML 维护。

远程域名集示例：

```yaml
data_providers:
  - tag: custom_domains
    file: ./rules/webui-custom_domains.txt
    url: https://example.net/domains.txt
    interval: 60 # 分钟，最小5；未设置时60
    auto_reload: true
```

支持纯域名、mosdns domain/full/keyword/regexp、Clash domain payload、两列 DOMAIN/DOMAIN-SUFFIX/DOMAIN-KEYWORD、简单 `||example.com^`。不支持二进制 GeoSite/SRS、带策略动作的完整 Clash 文件或复杂 AdBlock 例外；不支持的规则会报错，避免无声改变分流。下载失败保留上一份缓存；新规则集保存并应用后才成为运行中的 provider。

## 查询记录

日志表格显示实际返回 IP、响应码、耗时、缓存命中及上游组。详情包含 Answer 的名称、类型、TTL、值和有界处理轨迹。搜索支持域名、IP、上游、客户端；双引号为精确匹配。缓存响应不会伪造一次新的上游查询；并行/重试的轨迹可能含尝试路径。日志保存在内存，重启清空；80 个事件、64 条 Answer、每条文本约2 KiB、有界总大小不会改变客户端实际 DNS 响应。

## 校验与应用

`mosdns check -c config.yaml -d /etc/mosdns` 初始化 provider、插件与流水线引用，不绑定服务端口。配置保存使用该校验（配置 apply_command 时），应用脚本再启动服务并检查端口、API 和 DNS，失败恢复 `config.yaml.panel-last-good`。校验不能提前证明上游连通性或端口可用。

Debian/systemd 部署脚本安装独立的应用任务，避免重启 mosdns 时杀掉恢复程序：

```yaml
api:
  http: 192.168.50.110:9099
  webui: true
  allow_config_write: true
  audit_capacity: 3000
  apply_command:
    - /usr/bin/systemd-run
    - --quiet
    - --collect
    - --unit=mosdns-panel-apply
    - /bin/sh
    - /etc/mosdns/panel-apply.sh
```

独立测试可通过 `MOSDNS_SERVICE` 和 `MOSDNS_DNS_PORT` 指定测试 unit/端口。此任务实际验证了隔离实例端口冲突后的自动恢复，没有用生产服务做故意失败测试。

## 新增 API

| 方法 | 路径 | 内容 |
|---|---|---|
| POST | `/api/v1/config/format` | `{text}` → 保留注释的两空格 YAML |
| POST | `/api/v1/config` | `{text,sha256,validate_only,apply}` |
| GET | `/api/v1/config/apply-status` | idle/applying/applied/rolled_back/failed |
| GET | `/api/v1/rule-file?tag=...` | 内容、hash、类型、数量和订阅状态 |
| POST | `/api/v1/rule-file` | `{tag,text,sha256}`，更新运行规则 |
| POST | `/api/v1/rule-preview` | `{url}` → 有效数量和前20行 |
| POST | `/api/v1/rule-refresh` | `{tag}`，更新已加载订阅 |

所有修改仍通过现有本地管理端口；`allow_config_write` 控制规则与配置写入。此机器的面板仅开放 LAN，沿用原防火墙限制。未移植 AliAPI、完整对方 special_groups/SRS、上游热重载、二进制自动更新；这里的上游分组和远程域名订阅基于 mosdns-x 原生插件实现。

验证：前端构建、Go 全套测试通过；实际 Debian amd64 部署通过 UDP/TCP 国内外查询，隔离实例验证了远程分流、故障转移、旧缓存保留、规则/Hosts 更新、格式化、校验及应用回滚；浏览器验证草稿、差异、分流生成、大规则分页和规则保存。
