<script setup>
import { computed, ref } from 'vue'
import { getJSON, postJSON } from '../api/http'
import { openConfirm } from '../utils/confirm'
import FlowNode from './FlowNode.vue'
const props = defineProps({ editor: Object })
const emit = defineEmits(['error', 'notice'])
const localError = ref('')
function fail(e) { localError.value = e.message || String(e); emit('error', e) }
const mode = ref('sets'), providerForm = ref(null), preview = ref(null), working = ref(false), file = ref(null), routeForm = ref(null), matcherForm = ref(null)
const sequence = ref('main_sequence')
const rulePage = ref(1), ruleSearch = ref('')
const ruleLines = computed(() => (file.value?.text || '').split('\n'))
const matchingLines = computed(() => ruleSearch.value ? ruleLines.value.filter(s => s.toLowerCase().includes(ruleSearch.value.toLowerCase())) : ruleLines.value)
const rulePages = computed(() => Math.max(1, Math.ceil(matchingLines.value.length / 500)))
const visibleRuleText = computed({
  get: () => matchingLines.value.slice((rulePage.value - 1) * 500, rulePage.value * 500).join('\n'),
  set: value => {
    if (!file.value || ruleSearch.value) return
    const lines = ruleLines.value.slice(), start = (rulePage.value - 1) * 500
    lines.splice(start, Math.min(500, lines.length - start), ...value.split('\n'))
    file.value.text = lines.join('\n')
  }
})
const currentSequence = computed(() => props.editor.sequences.value.find(s => s.tag === sequence.value) || props.editor.sequences.value[0])
const plugins = computed(() => props.editor.config.value.plugins || [])
const routes = computed(() => {
  if (!currentSequence.value) return []
  return (currentSequence.value.args?.exec || []).map((s, i) => ({ step: s, index: i })).filter(r => typeof r.step === 'object' && /^web_route_/.test(r.step.if))
})
function providerEdit(p) { localError.value = ''; providerForm.value = p ? { ...p, existing: true } : { tag: '', url: '', interval: 60, existing: false }; preview.value = null }
async function previewURL() {
  working.value = true
  try { preview.value = await postJSON('/api/v1/rule-preview', { url: providerForm.value.url }) } catch (e) { fail(e) } finally { working.value = false }
}
function providerSave() {
  try {
    const f = providerForm.value
    if (!/^[A-Za-z][\w-]*$/.test(f.tag)) throw new Error('规则集标识必须以英文字母开头。')
    if (!/^https?:\/\//.test(f.url)) throw new Error('请输入 HTTP/HTTPS 规则地址。')
    if (!f.existing && props.editor.providers.value.some(p => p.tag === f.tag)) throw new Error('规则集标识已存在。')
    props.editor.mutate(doc => {
      const value = { tag: f.tag, file: f.file || `./rules/webui-${f.tag}.txt`, url: f.url, interval: Number(f.interval), auto_reload: true }
      if (f.existing) { const idx = props.editor.providers.value.findIndex(p => p.tag === f.tag); doc.setIn(['data_providers', idx], { ...props.editor.providers.value[idx], ...value }) }
      else { if (!doc.has('data_providers')) doc.set('data_providers', []); doc.get('data_providers').add(doc.createNode(value)) }
    })
    providerForm.value = null; emit('notice', '订阅已加入草稿；保存并应用后开始下载与定时更新。可在“添加分流”中指定上游组。')
  } catch (e) { fail(e) }
}
async function viewFile(p) {
  localError.value = ''
  rulePage.value = 1; ruleSearch.value = ''
  working.value = true
  try { file.value = { ...await getJSON(`/api/v1/rule-file?tag=${encodeURIComponent(p.tag)}`), remote: !!p.url, original: '' }; file.value.original = file.value.text }
  catch (e) { fail(e) } finally { working.value = false }
}
async function saveFile() {
  if (!await openConfirm('备份并更新此规则文件？文件更新立即生效。', { title: '更新规则集' })) return
  const target = file.value, submitted = target.text
  working.value = true
  try { const r = await postJSON('/api/v1/rule-file', { tag: target.tag, text: submitted, sha256: target.sha256 }); target.sha256 = r.sha256; target.original = submitted; emit('notice', `规则已更新并备份：${r.backup}`) }
  catch (e) { fail(e) } finally { working.value = false }
}
async function refreshProvider(p) {
  working.value = true
  try { await postJSON('/api/v1/rule-refresh', { tag: p.tag }); emit('notice', '远程规则已更新并加载。') } catch (e) { fail(e) } finally { working.value = false }
}
function routeEdit(r) {
  localError.value = ''
  const matcher = r ? plugins.value.find(p => p.tag === r.step.if) : null
  const domains = matcher?.args?.domain || []
  const source = domains.find(d => d.startsWith('provider:'))
  const steps = currentSequence.value?.args?.exec || []
  const beforeCache = steps.findIndex(s => /cache|ecs_auto/.test(JSON.stringify(s)))
  routeForm.value = { existing: !!r, index: r?.index, tag: r?.step.if || 'web_route_', source: source ? 'provider' : 'inline', provider: source?.slice(9) || props.editor.providers.value[0]?.tag || '', domains: domains.filter(d => !d.startsWith('provider:')).join('\n'), group: r?.step.exec?.find(s => props.editor.groups.value.some(g => g.tag === s)) || props.editor.groups.value[0]?.tag || '', position: beforeCache < 0 ? steps.length : beforeCache }
}
function routeSave() {
  try {
    const f = routeForm.value
    if (!/^web_route_[\w-]+$/.test(f.tag)) throw new Error('分流标识使用 web_route_ 前缀，并补充唯一名称。')
    if (!f.group || !currentSequence.value) throw new Error('请先配置上游组及流水线。')
    const domains = f.source === 'provider' ? [`provider:${f.provider}`] : f.domains.split('\n').map(s => s.trim()).filter(s => s && !s.startsWith('#'))
    if (!domains.length || (f.source === 'provider' && !f.provider)) throw new Error('请选择规则集或填写域名。')
    if (!f.existing && plugins.value.some(p => p.tag === f.tag)) throw new Error('分流标识已存在。')
    const seqTag = currentSequence.value.tag
    props.editor.mutate(doc => {
      if (f.existing) doc.setIn([...props.editor.pluginPath(f.tag), 'args', 'domain'], domains)
      else props.editor.insertPlugin(doc, { tag: f.tag, type: 'query_matcher', args: { domain: domains } })
      const idx = doc.get('plugins').items.findIndex(p => p.get('tag') === seqTag)
      const list = doc.getIn(['plugins', idx, 'args', 'exec'])
      const value = { if: f.tag, exec: [f.group, '_return'] }
      if (f.existing) doc.setIn(['plugins', idx, 'args', 'exec', f.index], value)
      else list.items.splice(Number(f.position), 0, doc.createNode(value))
    })
    routeForm.value = null; mode.value = 'flow'; emit('notice', '分流已加入草稿；执行顺序决定优先级。保存并应用后生效。')
  } catch (e) { fail(e) }
}
async function routeRemove(r) {
  if (!await openConfirm('删除此 Web 分流步骤？域名匹配器会保留，可在 YAML 中继续维护。', { title: '删除分流', tone: 'danger' })) return
  try { props.editor.mutate(doc => doc.getIn([...props.editor.pluginPath(currentSequence.value.tag), 'args', 'exec']).items.splice(r.index, 1)) } catch (e) { fail(e) }
}
function move(r, delta) {
  try { props.editor.mutate(doc => { const list = doc.getIn([...props.editor.pluginPath(currentSequence.value.tag), 'args', 'exec']).items; const next = r.index + delta; if (next >= 0 && next < list.length) { const item = list.splice(r.index, 1)[0]; list.splice(next, 0, item) } }) } catch (e) { fail(e) }
}
function editMatcher(p) { localError.value = ''; matcherForm.value = { tag: p.tag, domains: (p.args?.domain || []).join('\n') } }
function saveMatcher() {
  try { props.editor.mutate(doc => doc.setIn([...props.editor.pluginPath(matcherForm.value.tag), 'args', 'domain'], matcherForm.value.domains.split('\n').map(s => s.trim()).filter(Boolean))); matcherForm.value = null; emit('notice', '匹配器已更新到草稿。') } catch (e) { fail(e) }
}
</script>
<template>
  <section class="manager">
    <div class="section-head"><div><h2>规则与流量导向</h2><p>域名集 → 匹配条件 → 上游组；与 YAML 共用一份草稿。</p></div><div class="actions"><button class="btn secondary" @click="providerEdit(null)">＋ 远程规则集</button><button class="btn" @click="routeEdit(null)">＋ 添加分流</button></div></div>
    <div class="segmented"><button v-for="(label, id) in { sets: '规则集', flow: '流量导向', matchers: '域名匹配器' }" :key="id" :class="{ active: mode === id }" @click="mode = id">{{ label }}</button></div>
    <div v-if="mode === 'sets'" class="group-grid"><article v-for="p in editor.providers.value" :key="p.tag" class="panel group-card"><header><div class="group-icon">{{ p.url ? '↻' : '≡' }}</div><div><h3>{{ p.tag }}</h3><span class="badge">{{ p.url ? '远程订阅' : '本地规则' }}</span></div></header><p class="mono rule-location">{{ p.url || p.file }}</p><p class="muted">{{ p.url ? `每 ${p.interval || 60} 分钟更新 · 失败时保留上次规则` : '编辑文件并即时更新匹配器' }}</p><footer><button class="btn secondary" :disabled="working" @click="viewFile(p)">{{ p.url ? '预览 / 更新状态' : '查看与编辑' }}</button><template v-if="p.url"><button class="btn ghost" :disabled="working" @click="providerEdit(p)">订阅设置</button><button class="btn ghost" :disabled="working" @click="refreshProvider(p)">立即更新</button></template></footer></article></div>
    <div v-else-if="mode === 'flow'">
      <div class="panel flow-toolbar"><label>流水线<select v-model="sequence"><option v-for="s in editor.sequences.value" :value="s.tag" :key="s.tag">{{ s.tag }}</option></select></label><p>从上至下执行；匹配时进入分支，_return 结束处理。可直接更换绿色步骤的上游组。</p></div>
      <div v-if="routes.length" class="panel"><h3>Web 添加的分流优先级</h3><div v-for="r in routes" :key="r.step.if" class="route-row"><strong>{{ r.step.if }}</strong><span>→ {{ r.step.exec?.[0] }}</span><div class="actions"><button class="btn ghost" @click="move(r, -1)">↑</button><button class="btn ghost" @click="move(r, 1)">↓</button><button class="btn secondary" @click="routeEdit(r)">编辑</button><button class="btn ghost danger" @click="routeRemove(r)">删除</button></div></div></div>
      <div v-if="currentSequence" class="panel flow-panel"><div class="flow-entry">DNS 请求 → {{ currentSequence.tag }}</div><FlowNode :steps="currentSequence.args?.exec || []" :path="[...editor.pluginPath(currentSequence.tag), 'args', 'exec']" :editor="editor" @error="emit('error', $event)" @notice="emit('notice', $event)" /></div>
    </div>
    <div v-else class="group-grid"><article v-for="p in editor.matchers.value.filter(p => p.args?.domain)" :key="p.tag" class="panel group-card"><h3>{{ p.tag }}</h3><div class="rule-patterns"><code v-for="d in p.args.domain.slice(0, 8)" :key="d">{{ d }}</code><small v-if="p.args.domain.length > 8">另有 {{ p.args.domain.length - 8 }} 条</small></div><footer><button class="btn secondary" @click="editMatcher(p)">编辑域名 / 引用规则集</button></footer></article></div>
    <div v-if="providerForm" class="modal-mask" @click.self="providerForm = null"><form class="dialog" @submit.prevent="providerSave"><h2>远程域名规则集</h2><p class="muted">支持纯域名、mosdns 文本、Clash domain payload、DOMAIN 系列及简单 ||域名^ 格式。</p><label>规则集标识<input v-model="providerForm.tag" :disabled="providerForm.existing" required placeholder="例如 overseas_domains"></label><label>订阅 URL<input v-model="providerForm.url" required type="url"></label><label>更新间隔（分钟）<input v-model.number="providerForm.interval" type="number" min="5" required></label><button type="button" class="btn secondary" :disabled="working" @click="previewURL">{{ working ? '下载中…' : '测试下载与预览' }}</button><div v-if="preview" class="preview-box"><strong>{{ preview.count }} 条有效规则</strong><pre>{{ preview.preview }}</pre></div><footer class="dialog-footer"><button type="button" class="btn secondary" @click="providerForm = null">取消</button><button class="btn" :disabled="working">加入草稿</button></footer></form></div>
    <div v-if="file" class="modal-mask" @click.self="file = null"><section class="dialog wide"><div class="section-head"><div><h2>{{ file.tag }}</h2><p>{{ file.count }} 条 · {{ file.kind }} {{ file.read_only ? '· 只读预览' : '· 更新立即生效' }}</p></div><button class="btn ghost" @click="file = null">关闭</button></div><p v-if="file.status?.last_error" class="port-error">更新失败：{{ file.status.last_error }}；继续使用原规则。</p><p v-if="file.remote && file.status?.last_update" class="muted">最近更新：{{ new Date(file.status.last_update).toLocaleString() }}</p><div class="actions log-search"><input v-model="ruleSearch" class="search-input" aria-label="规则内容搜索" placeholder="搜索域名或 IP（搜索结果只读）" @input="rulePage = 1"><span class="muted">{{ matchingLines.length }} 行 · 每页 500 行</span></div><textarea v-model="visibleRuleText" class="code-editor" :readonly="file.read_only || !!ruleSearch || working" spellcheck="false" aria-label="规则文件内容"></textarea><div class="pagination"><span>{{ rulePage }} / {{ rulePages }} 页 {{ file.read_only ? '· 仅预览前 2 MiB' : '' }}</span><div class="actions"><button class="btn secondary" :disabled="rulePage <= 1" @click="rulePage--">上一页</button><button class="btn secondary" :disabled="rulePage >= rulePages" @click="rulePage++">下一页</button></div></div><p v-if="file.kind === 'hosts'" class="muted">Hosts 使用 mosdns 原生格式：域名 IP（例如 example.com 192.0.2.1）。</p><footer class="dialog-footer"><button class="btn secondary" @click="file = null">关闭</button><button v-if="!file.read_only" class="btn" :disabled="working || file.text === file.original" @click="saveFile">备份并更新规则</button></footer></section></div>
    <div v-if="routeForm" class="modal-mask" @click.self="routeForm = null"><form class="dialog" @submit.prevent="routeSave"><h2>{{ routeForm.existing ? '编辑分流' : '添加域名分流' }}</h2><label>分流标识<input v-model="routeForm.tag" required :disabled="routeForm.existing"></label><label>域名来源<select v-model="routeForm.source"><option value="provider">引用规则集</option><option value="inline">直接填写域名集</option></select></label><label v-if="routeForm.source === 'provider'">规则集<select v-model="routeForm.provider"><option v-for="p in editor.providers.value" :value="p.tag" :key="p.tag">{{ p.tag }}{{ p.url ? '（远程）' : '' }}</option></select></label><label v-else>域名（每行一条）<textarea v-model="routeForm.domains" class="code-editor short" placeholder="domain:example.com&#10;full:dns.example.net"></textarea></label><label>目标上游组<select v-model="routeForm.group" required><option v-for="g in editor.groups.value" :value="g.tag" :key="g.tag">{{ g.tag }}</option></select></label><label v-if="!routeForm.existing">插入位置<select v-model.number="routeForm.position"><option v-for="(s, i) in currentSequence?.args?.exec || []" :key="i" :value="i">第 {{ i + 1 }} 步之前：{{ typeof s === 'string' ? s : s.if }}</option><option :value="currentSequence?.args?.exec?.length || 0">流水线末尾</option></select></label><p class="helper">默认放在缓存处理之前；更早的 Hosts、学校或 Tailnet 分支仍有更高优先级。新分流命中后通过目标组解析并结束处理。</p><footer class="dialog-footer"><button type="button" class="btn secondary" @click="routeForm = null">取消</button><button class="btn">加入草稿</button></footer></form></div>
    <div v-if="matcherForm" class="modal-mask" @click.self="matcherForm = null"><form class="dialog" @submit.prevent="saveMatcher"><h2>{{ matcherForm.tag }}</h2><p class="muted">每行一条 domain:、full:、keyword:、regexp: 或 provider:规则集标识。</p><textarea v-model="matcherForm.domains" class="code-editor" aria-label="域名匹配条件"></textarea><footer class="dialog-footer"><button type="button" class="btn secondary" @click="matcherForm = null">取消</button><button class="btn">更新草稿</button></footer></form></div>
    <p v-if="localError && (file || providerForm || routeForm || matcherForm)" class="modal-error" role="alert">{{ localError }}</p>
  </section>
</template>
