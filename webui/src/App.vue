<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { getJSON, postJSON } from './api/http'
import DnsOverviewCard from './components/dashboard/DnsOverviewCard.vue'
import ConfirmBubbleHost from './components/ConfirmBubbleHost.vue'
import { openConfirm } from './utils/confirm'

const tabs = [
  { id: 'overview', label: '概览' }, { id: 'logs', label: '查询日志' },
  { id: 'upstream', label: '上游设置' }, { id: 'rules', label: '规则管理' },
  { id: 'cache', label: '缓存统计' }, { id: 'config', label: '配置管理' }
]
const active = ref('overview'), info = ref({}), runtime = ref({}), cache = ref({})
const logs = ref([]), page = ref(1), pages = ref(0), total = ref(0), query = ref('')
const capturing = ref(true), selected = ref(null), capacity = ref(0)
const notice = ref(''), error = ref(''), busy = ref(false), auto = ref(true)
const text = ref(''), digest = ref(''), loadedText = ref(''), file = ref('')
const configLoaded = ref(false), configBusy = ref(false), dirty = computed(() => text.value !== loadedText.value)
const forwarders = computed(() => (runtime.value.plugins || []).filter(p => p.type === 'fast_forward'))
const providers = computed(() => runtime.value.data_providers || [])
const matchers = computed(() => (runtime.value.plugins || []).filter(p => /matcher/.test(p.type)))
const theme = ref(localStorage.getItem('mosdns-theme') === 'dark' ? 'dark' : 'light')
let timer

function applyTheme() { document.documentElement.setAttribute('data-theme', theme.value); localStorage.setItem('mosdns-theme', theme.value) }
function toggleTheme() { theme.value = theme.value === 'light' ? 'dark' : 'light'; applyTheme() }
function message(value) { notice.value = value; error.value = '' }
function fail(e) { error.value = e.message || String(e); notice.value = '' }
function pretty(value) { return JSON.stringify(value, null, 2) }
function time(value) { return new Date(value).toLocaleString('zh-CN', { hour12: false }) }

async function refreshLogs(nextPage = page.value) {
  const params = new URLSearchParams({ page: String(nextPage), limit: '50' })
  let keyword = query.value.trim()
  if (keyword.startsWith('"') && keyword.endsWith('"') && keyword.length > 1) { keyword = keyword.slice(1, -1); params.set('exact', 'true') }
  if (keyword) params.set('q', keyword)
  const data = await getJSON(`/api/v2/audit/logs?${params}`)
  logs.value = data.logs; page.value = data.pagination.current_page
  pages.value = data.pagination.total_pages; total.value = data.pagination.total_items
}
async function refresh() {
  if (busy.value) return
  busy.value = true
  try {
    const [system, config, stats, status, size] = await Promise.all([
      getJSON('/api/v1/system/info'), getJSON('/api/v1/runtime/config'), getJSON('/api/v1/cache/stats'),
      getJSON('/api/v1/audit/status'), getJSON('/api/v1/audit/capacity')
    ])
    info.value = system; runtime.value = config; cache.value = stats; capturing.value = status.capturing; capacity.value = size.capacity
    if (active.value === 'logs') await refreshLogs()
    window.dispatchEvent(new CustomEvent('mosdns-log-refresh'))
    error.value = ''
  } catch (e) { fail(e) } finally { busy.value = false }
}
async function switchTab(id) {
  if (active.value === 'config' && dirty.value && id !== 'config') {
    if (!await openConfirm('配置尚未保存，继续切换？编辑内容会保留。', { title: '未保存的配置' })) return
  }
  active.value = id; selected.value = null; notice.value = ''
  if (id === 'config' && info.value.config_write && !configLoaded.value) await loadConfig()
  else await refresh()
}
async function search() { try { await refreshLogs(1) } catch (e) { fail(e) } }
async function changePage(value) { try { await refreshLogs(value) } catch (e) { fail(e) } }
async function auditAction(action) {
  if (action === 'clear' && !await openConfirm('清空当前保留的查询日志？进程累计统计会保留。', { title: '清空日志', tone: 'danger' })) return
  try { await postJSON(`/api/v1/audit/${action}`, {}); page.value = 1; await refresh() } catch (e) { fail(e) }
}
async function loadConfig() {
  if (dirty.value && !await openConfirm('重新读取会丢弃尚未保存的编辑内容，继续？', { title: '重新读取' })) return
  configBusy.value = true
  try {
    const data = await getJSON('/api/v1/config')
    text.value = data.text; loadedText.value = data.text; digest.value = data.sha256; file.value = data.file; configLoaded.value = true
    message('已读取磁盘配置。')
  } catch (e) { fail(e) } finally { configBusy.value = false }
}
async function saveConfig(validateOnly = false) {
  if (!validateOnly && !await openConfirm('保存到主配置文件并创建备份？保存后请重启服务生效。', { title: '保存配置' })) return
  configBusy.value = true
  try {
    const data = await postJSON('/api/v1/config', { text: text.value, sha256: digest.value, validate_only: validateOnly })
    if (validateOnly) message('基础校验通过。监听端口、证书、文件和插件运行条件将在重启时检查。')
    else { digest.value = data.sha256; loadedText.value = text.value; message(`已保存；备份：${data.backup}。请重启 mosdns-x 服务生效。`) }
  } catch (e) { fail(e) } finally { configBusy.value = false }
}
function preventUnload(event) { if (dirty.value) { event.preventDefault(); event.returnValue = '' } }
onMounted(() => {
  applyTheme(); refresh()
  timer = window.setInterval(() => { if (auto.value && !document.hidden && active.value !== 'config') refresh() }, 5000)
  window.addEventListener('beforeunload', preventUnload)
})
onBeforeUnmount(() => { window.clearInterval(timer); window.removeEventListener('beforeunload', preventUnload) })
</script>

<template>
  <div class="app-shell">
    <div class="top-strip">
      <div class="top-strip-head">
        <header class="hero compact"><h1>MosDNS-X 仪表盘</h1></header>
        <div class="actions"><label><input v-model="auto" type="checkbox"> 自动刷新</label><button class="btn secondary" @click="toggleTheme">{{ theme === 'light' ? '深色' : '浅色' }}</button><button class="btn" :disabled="busy" @click="refresh">刷新</button></div>
      </div>
      <nav class="legacy-main-nav compact"><button v-for="tab in tabs" :key="tab.id" class="legacy-main-btn" :class="{ active: active === tab.id }" @click="switchTab(tab.id)">{{ tab.label }}</button></nav>
    </div>
    <main class="main-body">
      <p v-if="error" class="panel port-error" role="alert">{{ error }}</p>
      <p v-if="notice" class="panel port-notice" role="status">{{ notice }}</p>
      <section v-if="active === 'overview'" class="page-shell">
        <DnsOverviewCard />
        <div class="panel"><h3>运行状态</h3><div class="port-grid"><p>版本：{{ info.version || '-' }}</p><p>平台：{{ info.platform || '-' }}</p><p>运行时间：{{ Math.floor(info.uptime_seconds || 0) }} 秒</p><p>Go 内存：{{ ((info.memory_bytes || 0) / 1048576).toFixed(1) }} MiB</p><p>Goroutines：{{ info.goroutines || 0 }}</p><p>日志：{{ capturing ? '记录中' : '已暂停' }} / 最多 {{ capacity }} 条</p></div><p class="muted">累计统计覆盖本次进程运行；时间段统计仅覆盖保留日志。趋势按 3 秒采样，暂停日志后累计统计仍继续。</p></div>
      </section>
      <section v-else-if="active === 'logs'" class="page-shell panel">
        <header class="panel-header"><div><h3>查询日志</h3><p class="muted">显示客户端实际收到的响应；入口表示服务器使用的流水线。</p></div><div class="actions"><button class="btn secondary" @click="auditAction(capturing ? 'stop' : 'start')">{{ capturing ? '暂停记录' : '开始记录' }}</button><button class="btn danger" @click="auditAction('clear')">清空日志</button></div></header>
        <form class="actions" @submit.prevent="search"><input v-model="query" class="port-search" aria-label="日志关键字" placeholder="域名、客户端 IP、类型、入口；双引号精确匹配"><button class="btn">搜索</button></form>
        <div class="table-wrap"><table><thead><tr><th>时间</th><th>客户端</th><th>域名 / 类型</th><th>入口</th><th>响应</th><th>耗时</th></tr></thead><tbody><tr v-for="log in logs" :key="log.trace_id" class="port-log-row" tabindex="0" @click="selected = log" @keydown.enter="selected = log"><td>{{ time(log.query_time) }}</td><td class="mono">{{ log.client_ip || '-' }}</td><td class="mono">{{ log.query_name }} <span class="muted">{{ log.query_type }}</span></td><td>{{ log.entry }}</td><td>{{ log.response_code }}</td><td>{{ log.duration_ms.toFixed(2) }} ms</td></tr><tr v-if="!logs.length"><td colspan="6">暂无匹配日志。向 DNS 监听端口发送请求后刷新。</td></tr></tbody></table></div>
        <div class="actions"><button class="btn secondary" :disabled="page <= 1" @click="changePage(page - 1)">上一页</button><span>{{ page }} / {{ Math.max(1, pages) }} 页，共 {{ total }} 条</span><button class="btn secondary" :disabled="page >= pages" @click="changePage(page + 1)">下一页</button></div>
        <div v-if="selected" class="panel port-details"><div class="actions"><h3>响应详情：{{ selected.query_name }}</h3><button class="btn secondary" @click="selected = null">关闭</button></div><p>协议：{{ selected.protocol || '-' }}；响应码：{{ selected.response_code }}；Trace ID：{{ selected.trace_id }}</p><pre>{{ selected.answers.map(a => a.data).join('\n') || '无 Answer 记录' }}</pre><p v-if="selected.answers_truncated" class="muted">响应详情过长，日志展示已截断；实际 DNS 响应保持完整。</p></div>
      </section>
      <section v-else-if="active === 'upstream'" class="page-shell panel"><h3>上游设置</h3><p class="muted">当前进程加载的 fast_forward 配置。可在「配置管理」修改，重启后生效。</p><div v-for="plugin in forwarders" :key="plugin.tag" class="panel"><h4>{{ plugin.tag }}</h4><pre>{{ pretty(plugin.args) }}</pre></div><p v-if="!forwarders.length">当前未配置 fast_forward 插件。</p></section>
      <section v-else-if="active === 'rules'" class="page-shell panel"><h3>规则管理</h3><p class="muted">当前进程加载的数据源和匹配器；自动重载由 data_providers.auto_reload 控制。可在「配置管理」修改配置。</p><h4>数据源</h4><pre>{{ pretty(providers) }}</pre><h4>匹配器</h4><pre>{{ pretty(matchers) }}</pre><h4>流水线</h4><pre>{{ pretty((runtime.plugins || []).filter(p => p.type === 'sequence')) }}</pre></section>
      <section v-else-if="active === 'cache'" class="page-shell panel"><h3>缓存统计</h3><p class="muted">来自 mosdns-x 缓存插件的实时指标，包含预置缓存。</p><div class="table-wrap"><table><thead><tr><th>插件</th><th>记录数</th><th>请求数</th><th>命中数</th><th>过期命中数</th><th>命中率</th></tr></thead><tbody><tr v-for="(stats, tag) in cache" :key="tag"><td>{{ tag }}</td><td>{{ stats.cache_size || 0 }}</td><td>{{ stats.query_total || 0 }}</td><td>{{ stats.hit_total || 0 }}</td><td>{{ stats.lazy_hit_total || 0 }}</td><td>{{ stats.query_total ? (stats.hit_total / stats.query_total * 100).toFixed(1) : '0.0' }}%</td></tr></tbody></table></div></section>
      <section v-else class="page-shell panel"><h3>配置管理</h3><p class="muted">编辑主配置文件；include 文件单独维护。保存会创建备份，并检查文件是否被其他编辑器修改。运行配置在重启后更新。</p><p v-if="!info.config_write">配置编辑未开启。请在主配置中设置 api.allow_config_write: true，并重启服务。</p><template v-else><div class="actions"><strong>{{ file }}</strong><span v-if="dirty">未保存</span><button class="btn secondary" :disabled="configBusy" @click="loadConfig">重新读取</button><button class="btn secondary" :disabled="configBusy || !configLoaded" @click="saveConfig(true)">校验</button><button class="btn" :disabled="configBusy || !configLoaded || !dirty" @click="saveConfig(false)">保存并备份</button></div><textarea v-model="text" class="port-editor mono" aria-label="主配置文件" spellcheck="false" :disabled="configBusy || !configLoaded"></textarea></template></section>
    </main>
    <ConfirmBubbleHost />
  </div>
</template>

<style scoped>
.port-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 8px; }
.port-error { color: #df3546; }.port-notice { color: #168757; }
.port-search { flex: 1; min-width: 200px; }.port-log-row { cursor: pointer; }.port-log-row:hover { background: var(--bg-hover, #8882); }
.port-editor { display: block; width: 100%; min-height: 55vh; margin-top: 16px; padding: 14px; resize: vertical; tab-size: 2; }
@media (max-width: 760px) {
  .top-strip-head { flex-wrap: wrap; }
  .hero.compact { max-width: 100%; }
  .hero.compact h1 { white-space: nowrap; }
  .top-strip-head > .actions { margin-left: auto; }
}
pre { overflow: auto; white-space: pre-wrap; overflow-wrap: break-word; }.table-wrap { width: 100%; overflow-x: auto; margin: 16px 0; }table { min-width: 650px; width: 100%; }td, th { padding: 10px; text-align: left; }.port-details { margin-top: 16px; }
</style>
