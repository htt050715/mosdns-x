<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { getJSON, postJSON } from './api/http'
import { useConfig } from './composables/useConfig'
import DnsOverviewCard from './components/dashboard/DnsOverviewCard.vue'
import UpstreamManager from './components/UpstreamManager.vue'
import RulesManager from './components/RulesManager.vue'
import ConfirmBubbleHost from './components/ConfirmBubbleHost.vue'
import { openConfirm } from './utils/confirm'
const tabs = [{ id: 'overview', label: '运行概览', icon: '◫' }, { id: 'logs', label: '查询日志', icon: '≡' }, { id: 'upstream', label: '上游分组', icon: '↗' }, { id: 'rules', label: '规则与导向', icon: '⑂' }, { id: 'cache', label: '缓存统计', icon: '▦' }, { id: 'config', label: '配置编辑', icon: '{ }' }]
const active = ref('overview'), info = ref({}), cache = ref({}), logs = ref([]), page = ref(1), pages = ref(0), total = ref(0), query = ref('')
const capturing = ref(true), capacity = ref(0), selected = ref(null), notice = ref(''), error = ref(''), busy = ref(false), auto = ref(true)
const theme = ref(localStorage.getItem('mosdns-theme') === 'dark' ? 'dark' : 'light')
function message(v) { notice.value = v; error.value = '' }
function fail(e) { error.value = e.message || String(e); notice.value = '' }
const editor = useConfig(message, fail)
const title = computed(() => tabs.find(t => t.id === active.value)?.label)
const applying = computed(() => editor.applyState.value?.state === 'applying')
let timer
function time(v) { return new Date(v).toLocaleString('zh-CN', { hour12: false }) }
function answers(log) { return (log.answers || []).filter(a => ['A', 'AAAA'].includes(a.type)).map(a => a.value || a.data).join(' · ') }
function applyTheme() { document.documentElement.setAttribute('data-theme', theme.value); localStorage.setItem('mosdns-theme', theme.value) }
function toggleTheme() { theme.value = theme.value === 'light' ? 'dark' : 'light'; applyTheme() }
async function refreshLogs(next = page.value) {
  const params = new URLSearchParams({ page: String(next), limit: '50' })
  let keyword = query.value.trim()
  if (keyword.startsWith('"') && keyword.endsWith('"') && keyword.length > 1) { keyword = keyword.slice(1, -1); params.set('exact', 'true') }
  if (keyword) params.set('q', keyword)
  const data = await getJSON(`/api/v2/audit/logs?${params}`)
  logs.value = data.logs; page.value = data.pagination.current_page; pages.value = data.pagination.total_pages; total.value = data.pagination.total_items
}
async function refresh() {
  if (busy.value || applying.value) return
  busy.value = true
  try {
    const [system, stats, status, size] = await Promise.all([getJSON('/api/v1/system/info'), getJSON('/api/v1/cache/stats'), getJSON('/api/v1/audit/status'), getJSON('/api/v1/audit/capacity')])
    info.value = system; cache.value = stats; capturing.value = status.capturing; capacity.value = size.capacity
    if (active.value === 'logs') await refreshLogs()
    window.dispatchEvent(new CustomEvent('mosdns-log-refresh'))
  } catch (e) { fail(e) } finally { busy.value = false }
}
async function switchTab(id) { active.value = id; selected.value = null; if (['upstream', 'rules', 'config'].includes(id)) await editor.load(); else await refresh() }
async function search(next = 1) { try { await refreshLogs(next) } catch (e) { fail(e) } }
async function auditAction(action) {
  if (action === 'clear' && !await openConfirm('清空当前保留的查询日志？进程累计统计会保留。', { title: '清空日志', tone: 'danger' })) return
  try { await postJSON(`/api/v1/audit/${action}`, {}); page.value = 1; await refresh() } catch (e) { fail(e) }
}
async function reloadConfig() {
  if (editor.dirty.value && !await openConfirm('重新读取会丢弃尚未保存的草稿，继续？', { title: '重新读取配置' })) return
  await editor.load(true)
}
async function save(apply = false) {
  if (!await openConfirm(apply ? '备份配置并应用？服务会短暂重启，失败时自动恢复上一份生效配置。' : '备份并保存配置？当前服务将在下次应用时加载这份配置。', { title: apply ? '保存并应用配置' : '保存配置' })) return
  await editor.save(false, apply)
}
function preventUnload(e) { if (editor.dirty.value) { e.preventDefault(); e.returnValue = '' } }
onMounted(async () => {
  applyTheme(); await refresh(); await editor.load()
  timer = window.setInterval(async () => {
    if (applying.value) { if (await editor.pollApply()) await refresh(); return }
    if (auto.value && !document.hidden) refresh()
  }, 3000)
  window.addEventListener('beforeunload', preventUnload)
})
onBeforeUnmount(() => { window.clearInterval(timer); window.removeEventListener('beforeunload', preventUnload) })
</script>
<template>
  <div class="management-shell">
    <aside class="sidebar"><a class="brand-lockup" href="#" @click.prevent="switchTab('overview')"><span class="brand-mark">m<span>×</span></span><div><strong>MosDNS-X</strong><small>DNS 控制台</small></div></a><p class="nav-caption">工作空间</p><nav><button v-for="tab in tabs" :key="tab.id" :class="{ active: active === tab.id }" @click="switchTab(tab.id)"><span>{{ tab.icon }}</span>{{ tab.label }}<i v-if="['upstream', 'rules', 'config'].includes(tab.id) && editor.dirty.value" class="draft-dot"></i></button></nav><div class="sidebar-foot"><span class="status-dot"></span><span>本地 DNS 服务<small>{{ info.version || '连接中…' }} · {{ info.platform }}</small></span></div></aside>
    <main class="console-main">
      <header class="console-header"><div><p class="eyebrow">网络 / DNS</p><h1>{{ title }}</h1></div><div class="actions"><label class="check-label"><input v-model="auto" type="checkbox">自动刷新</label><button class="btn secondary" @click="toggleTheme">{{ theme === 'light' ? '深色' : '浅色' }}</button><button class="btn secondary" :disabled="busy || applying" @click="refresh">刷新状态</button></div></header>
      <p v-if="error" class="alert error" role="alert">{{ error }}<button @click="error = ''" aria-label="关闭提示">×</button></p><p v-if="notice" class="alert success" role="status">{{ notice }}<button @click="notice = ''" aria-label="关闭提示">×</button></p>
      <div v-if="applying" class="alert progress" role="status">正在应用配置并检查 DNS… 服务重启期间页面会自动重连。</div>
      <div v-if="['upstream', 'rules', 'config'].includes(active)" class="draft-bar"><div><span class="badge" :class="{ warning: editor.dirty.value }">{{ editor.dirty.value ? '未保存草稿' : '配置已同步' }}</span><small>表单与 YAML 同步 · 保存时自动备份</small></div><div class="actions"><button class="btn ghost" :disabled="editor.busy.value || applying" @click="reloadConfig">重新读取</button><button class="btn secondary" :disabled="editor.busy.value || applying || !info.config_write" @click="editor.save(true)">校验</button><button class="btn secondary" :disabled="!editor.dirty.value || applying" @click="editor.review.value = true">查看差异</button><button class="btn" :disabled="editor.busy.value || applying || !info.config_write || !info.can_apply" @click="save(true)">保存并应用</button></div></div>
      <p v-if="editor.parseError.value && ['upstream', 'rules', 'config'].includes(active)" class="alert error">YAML 语法错误：{{ editor.parseError.value }}</p>
      <section v-if="active === 'overview'" class="overview-page"><div class="quick-metrics"><article class="panel"><span>运行时间</span><strong>{{ Math.floor((info.uptime_seconds || 0) / 3600) }}<small>小时</small> {{ Math.floor((info.uptime_seconds || 0) % 3600 / 60) }}<small>分钟</small></strong></article><article class="panel"><span>内存占用</span><strong>{{ ((info.memory_bytes || 0) / 1048576).toFixed(1) }}<small>MiB</small></strong></article><article class="panel"><span>上游分组</span><strong>{{ editor.groups.value.length }}<small>组</small></strong></article><article class="panel"><span>日志记录</span><strong>{{ capturing ? '进行中' : '已暂停' }}</strong><small>保留 {{ capacity }} 条查询</small></article></div><DnsOverviewCard /><div class="panel overview-links"><div><h3>把分流规则变成可操作的配置</h3><p class="muted">编辑上游分组、订阅域名集，并在执行流程中选择每个分支的目标。</p></div><button class="btn secondary" @click="switchTab('rules')">管理流量导向 →</button></div></section>
      <section v-else-if="active === 'logs'" class="panel logs-panel"><div class="section-head"><div><h2>查询记录</h2><p>查看实际返回的 IP、记录 TTL 和处理路径。</p></div><div class="actions"><button class="btn secondary" @click="auditAction(capturing ? 'stop' : 'start')">{{ capturing ? '暂停记录' : '开始记录' }}</button><button class="btn ghost danger" @click="auditAction('clear')">清空</button></div></div><form class="actions log-search" @submit.prevent="search()"><input v-model="query" class="search-input" aria-label="查询日志搜索" placeholder="搜索域名、客户端、返回 IP 或上游；双引号精确匹配"><button class="btn secondary">搜索</button></form><div class="table-wrap"><table><thead><tr><th>查询 / 客户端</th><th>返回 IP / 记录</th><th>处理路径</th><th>结果</th><th>耗时 / 时间</th></tr></thead><tbody><tr v-for="log in logs" :key="log.trace_id" class="clickable" tabindex="0" @click="selected = log" @keydown.enter="selected = log"><td><strong class="mono">{{ log.query_name }}</strong><small>{{ log.query_type }} · {{ log.client_ip || '-' }}</small></td><td class="answer-cell"><span class="mono">{{ answers(log) || log.answers?.map(a => a.value || a.data).join(' · ') || '无 Answer 记录' }}</span></td><td><span class="badge" :class="{ subtle: !!log.cache }">{{ log.cache ? `缓存 ${log.cache}` : log.group || log.entry }}</span><small class="mono">{{ log.upstream || (log.cache ? '缓存命中' : log.entry) }}</small></td><td><span class="badge" :class="{ warning: log.response_code !== 'NOERROR' }">{{ log.response_code }}</span></td><td><strong>{{ log.duration_ms.toFixed(2) }} ms</strong><small>{{ time(log.query_time) }}</small></td></tr><tr v-if="!logs.length"><td colspan="5" class="empty-state">暂无匹配记录，向此 DNS 发送请求后刷新。</td></tr></tbody></table></div><div class="pagination"><span>{{ total }} 条记录 · 第 {{ page }} / {{ Math.max(1, pages) }} 页</span><div class="actions"><button class="btn secondary" :disabled="page <= 1" @click="search(page - 1)">上一页</button><button class="btn secondary" :disabled="page >= pages" @click="search(page + 1)">下一页</button></div></div></section>
      <UpstreamManager v-else-if="active === 'upstream' && !editor.parseError.value" :editor="editor" @notice="message" @error="fail" />
      <RulesManager v-else-if="active === 'rules' && !editor.parseError.value" :editor="editor" @notice="message" @error="fail" />
      <section v-else-if="active === 'cache'" class="panel"><div class="section-head"><div><h2>缓存插件</h2><p>当前进程的实时统计，包含预置缓存。</p></div></div><div class="table-wrap"><table><thead><tr><th>插件</th><th>记录数</th><th>请求</th><th>命中</th><th>过期命中</th><th>命中率</th></tr></thead><tbody><tr v-for="(stats, tag) in cache" :key="tag"><td class="mono">{{ tag }}</td><td>{{ stats.cache_size || 0 }}</td><td>{{ stats.query_total || 0 }}</td><td>{{ stats.hit_total || 0 }}</td><td>{{ stats.lazy_hit_total || 0 }}</td><td>{{ stats.query_total ? (stats.hit_total / stats.query_total * 100).toFixed(1) : '0.0' }}%</td></tr></tbody></table></div></section>
      <section v-else-if="active === 'config'" class="panel"><div class="section-head"><div><h2>YAML 配置</h2><p>支持注释保留与两空格格式化；保存前检查插件和引用。</p></div><div class="actions"><button class="btn secondary" :disabled="editor.busy.value" @click="editor.format">格式化</button><button class="btn secondary" :disabled="editor.busy.value || applying || !info.config_write || !editor.dirty.value" @click="save(false)">仅保存</button></div></div><textarea v-model="editor.text.value" class="code-editor main-editor" :disabled="!info.config_write || editor.busy.value || applying" spellcheck="false" aria-label="YAML 主配置"></textarea></section>
    </main>
    <div v-if="selected" class="modal-mask" @click.self="selected = null"><section class="dialog wide query-dialog"><div class="section-head"><div><p class="eyebrow">查询详情 · {{ selected.trace_id }}</p><h2 class="mono">{{ selected.query_name }}</h2><p>{{ selected.query_type }} · {{ selected.client_ip }} · {{ selected.protocol }} · {{ selected.duration_ms.toFixed(2) }} ms</p></div><button class="btn ghost" @click="selected = null">关闭</button></div><div class="detail-summary"><span class="badge">{{ selected.response_code }}</span><span>上游组 {{ selected.group || '-' }}</span><span class="mono">{{ selected.upstream || '-' }}</span><span v-if="selected.cache">缓存 {{ selected.cache }}</span></div><p v-if="selected.error" class="alert error">{{ selected.error }}</p><h3>Answer 记录</h3><div class="table-wrap"><table><thead><tr><th>名称</th><th>类型</th><th>TTL</th><th>值</th></tr></thead><tbody><tr v-for="(a, i) in selected.answers" :key="i"><td class="mono">{{ a.name }}</td><td>{{ a.type }}</td><td>{{ a.ttl }} s</td><td class="mono">{{ a.value || a.data }}</td></tr><tr v-if="!selected.answers?.length"><td colspan="4">响应没有 Answer 记录。</td></tr></tbody></table></div><p v-if="selected.answers_truncated" class="muted">日志中的长响应已截断，实际 DNS 响应保持完整。</p><h3>处理轨迹</h3><p class="muted">按事件发生顺序显示；并行分支可能包含尝试路径。</p><ol class="trace-list"><li v-for="(event, i) in selected.trace" :key="i"><span class="badge">{{ { match: '匹配', step: '执行', upstream: '上游', group: '上游组', cache: '缓存', error: '错误' }[event.kind] || event.kind }}</span><strong class="mono">{{ event.tag }}</strong><span class="mono">{{ event.detail }}</span></li></ol></section></div>
    <div v-if="editor.review.value" class="modal-mask" @click.self="editor.review.value = false"><section class="dialog wide"><div class="section-head"><div><h2>配置差异</h2><p>从第 {{ editor.delta.value.line }} 行开始的变更区域（包含中间未变化行）。</p></div><button class="btn ghost" @click="editor.review.value = false">关闭</button></div><div class="diff-grid"><div><h3>保存前</h3><pre class="diff-old">{{ editor.delta.value.removed || '无删除内容' }}</pre></div><div><h3>草稿</h3><pre class="diff-new">{{ editor.delta.value.added || '无新增内容' }}</pre></div></div><footer class="dialog-footer"><button class="btn secondary" @click="editor.review.value = false">继续编辑</button><button class="btn" :disabled="editor.busy.value || !info.can_apply" @click="save(true)">保存并应用</button></footer></section></div>
    <ConfirmBubbleHost />
  </div>
</template>
