<script setup>
import { computed, ref } from 'vue'
import { openConfirm } from '../utils/confirm'
const props = defineProps({ editor: Object })
const emit = defineEmits(['error', 'notice'])
const localError = ref('')
function fail(e) { localError.value = e.message || String(e); emit('error', e) }
const form = ref(null), advanced = ref(-1), search = ref('')
const filtered = computed(() => props.editor.groups.value.filter(g => JSON.stringify(g).toLowerCase().includes(search.value.toLowerCase())))
const strategyNames = { parallel: '并发择优', fallback: '顺序故障转移', round_robin: '轮询 + 故障转移' }
function edit(group) {
  localError.value = ''
  form.value = group ? { tag: group.tag, existing: true, args: structuredClone(group.args || {}) } : { tag: '', existing: false, args: { strategy: 'parallel', upstream: [{ addr: '', bootstrap: '', trusted: true }] } }
  if (!form.value.args.strategy) form.value.args.strategy = 'parallel'
  if (!form.value.args.upstream) form.value.args.upstream = []
  advanced.value = -1
}
function submit() {
  try {
    const f = form.value
    if (!/^[A-Za-z][\w-]*$/.test(f.tag)) throw new Error('组名使用英文字母开头，可包含数字、下划线和连字符。')
    if (!f.args.upstream.length || f.args.upstream.some(u => !u.addr.trim())) throw new Error('至少添加一个有效的上游地址。')
    if (!f.existing && props.editor.config.value.plugins?.some(p => p.tag === f.tag)) throw new Error('该组名已被使用。')
    props.editor.mutate(doc => {
      if (f.existing) doc.setIn([...props.editor.pluginPath(f.tag), 'args'], f.args)
      else props.editor.insertPlugin(doc, { tag: f.tag, type: 'fast_forward', args: f.args })
    })
    form.value = null; emit('notice', '上游组已更新到草稿，保存并应用后生效。')
  } catch (e) { fail(e) }
}
async function remove(g) {
  if (!await openConfirm(`删除上游组 ${g.tag}？请确保所有规则已改用其他组。`, { title: '删除上游组', tone: 'danger' })) return
  try {
    const ownIndex = props.editor.pluginPath(g.tag)[1]
    const referenced = (props.editor.config.value.plugins || []).some((p, i) => i !== ownIndex && JSON.stringify(p.args).includes(`"${g.tag}"`)) || (props.editor.config.value.servers || []).some(s => s.exec === g.tag)
    if (referenced) throw new Error('该组仍被规则或监听入口引用，请先更改流量导向。')
    props.editor.mutate(doc => doc.get('plugins').items.splice(ownIndex, 1)); emit('notice', '删除已加入草稿。')
  } catch (e) { fail(e) }
}
</script>
<template>
  <section class="manager">
    <div class="section-head"><div><h2>上游组</h2><p>把 DNS 服务器归入组，再让规则选择对应的组。</p></div><button class="btn" @click="edit(null)">＋ 新建上游组</button></div>
    <input v-model="search" class="search-input" aria-label="搜索上游组" placeholder="搜索组名或服务器地址">
    <div class="group-grid"><article v-for="g in filtered" :key="g.tag" class="panel group-card">
      <header><div class="group-icon">↗</div><div><h3>{{ g.tag }}</h3><span class="badge">{{ strategyNames[g.args?.strategy || 'parallel'] }}</span></div><span class="member-count">{{ g.args?.upstream?.length || 0 }} 个上游</span></header>
      <ul class="upstream-list"><li v-for="(u, i) in g.args?.upstream" :key="i"><span class="status-dot"></span><div><strong class="mono">{{ u.addr }}</strong><small>{{ u.bootstrap ? `引导 DNS ${u.bootstrap}` : '使用系统解析' }}{{ u.dial_addr ? ` · 拨号 ${u.dial_addr}` : '' }}</small></div><span class="badge subtle">{{ u.addr.split('://')[0] === u.addr ? 'UDP' : u.addr.split('://')[0].toUpperCase() }}</span></li></ul>
      <footer><button class="btn secondary" @click="edit(g)">编辑组与成员</button><button class="btn ghost danger" @click="remove(g)">删除</button></footer>
    </article></div>
    <p v-if="!filtered.length" class="empty-state">还没有上游组，点击右上角添加。</p>
    <div v-if="form" class="modal-mask" @click.self="form = null"><form class="dialog wide" @submit.prevent="submit">
      <header class="section-head"><div><h2>{{ form.existing ? '编辑上游组' : '新建上游组' }}</h2><p>一个组可以包含 UDP、TCP、DoH、DoT、DoQ 等不同类型的服务器。</p></div><button type="button" class="btn ghost" @click="form = null">关闭</button></header>
      <div class="form-grid"><label>组名<input v-model="form.tag" :disabled="form.existing" required placeholder="例如 domestic_dns"></label><label>选择策略<select v-model="form.args.strategy"><option value="parallel">并发择优：保留 mosdns-x 原行为</option><option value="fallback">顺序故障转移：按列表顺序尝试</option><option value="round_robin">轮询：每个请求轮换首选服务器</option></select></label></div>
      <p class="helper">并发返回最先接受的结果；列表第一个上游自动视为可信。故障转移和轮询每个服务器最多等待 2 秒，总耗时受查询超时限制。</p>
      <div v-for="(u, i) in form.args.upstream" :key="i" class="member-editor">
        <div class="section-head"><strong>上游 {{ i + 1 }}</strong><div class="actions"><button type="button" class="btn ghost" @click="advanced = advanced === i ? -1 : i">高级设置</button><button type="button" class="btn ghost danger" @click="form.args.upstream.splice(i, 1)">移除</button></div></div>
        <div class="form-grid"><label>服务器地址<input v-model="u.addr" required placeholder="223.5.5.5 / https://dns.example/dns-query"></label><label>引导 DNS<input v-model="u.bootstrap" placeholder="DoH 域名解析使用，例如 223.5.5.5"></label></div>
        <div v-if="advanced === i" class="form-grid"><label>拨号地址<input v-model="u.dial_addr" placeholder="指定连接的 IP:端口"></label><label>SOCKS5 代理<input v-model="u.socks5" placeholder="127.0.0.1:1080"></label><label>代理用户名<input v-model="u.s5_username" autocomplete="off"></label><label>代理密码<input v-model="u.s5_password" type="password" autocomplete="new-password"></label><label>空闲超时（秒）<input v-model.number="u.idle_timeout" type="number" min="0"></label><label>最大连接数<input v-model.number="u.max_conns" type="number" min="0"></label><label>网卡绑定<input v-model="u.bind_to_device"></label><label>SO_MARK<input v-model.number="u.so_mark" type="number" min="0"></label><label class="check-label"><input v-model="u.trusted" type="checkbox">接受该上游的非成功响应</label><label class="check-label"><input v-model="u.enable_pipeline" type="checkbox">启用连接流水线</label><label class="check-label"><input v-model="u.insecure" type="checkbox">跳过 TLS 证书校验</label></div>
      </div>
      <button type="button" class="btn secondary" @click="form.args.upstream.push({ addr: '', bootstrap: '' })">＋ 添加组内上游</button>
      <footer class="dialog-footer"><span>修改会加入配置草稿。</span><button type="button" class="btn secondary" @click="form = null">取消</button><button class="btn">更新草稿</button></footer>
    </form></div>
    <p v-if="localError && form" class="modal-error" role="alert">{{ localError }}</p>
  </section>
</template>
