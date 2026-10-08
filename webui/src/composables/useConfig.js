import { computed, ref } from 'vue'
import { parseDocument } from 'yaml'
import { getJSON, postJSON } from '../api/http'

export function useConfig(notify, fail) {
  const text = ref(''), original = ref(''), digest = ref(''), busy = ref(false), loaded = ref(false)
  const applyState = ref(null), review = ref(false)
  const document = computed(() => parseDocument(text.value, { prettyErrors: true }))
  const parseError = computed(() => document.value.errors.map(e => e.message).join('\n'))
  const config = computed(() => parseError.value ? {} : (document.value.toJS() || {}))
  const dirty = computed(() => text.value !== original.value)
  const groups = computed(() => (config.value.plugins || []).filter(p => p.type === 'fast_forward'))
  const providers = computed(() => config.value.data_providers || [])
  const sequences = computed(() => (config.value.plugins || []).filter(p => p.type === 'sequence'))
  const matchers = computed(() => (config.value.plugins || []).filter(p => p.type === 'query_matcher'))
  const delta = computed(() => {
    const a = original.value.split('\n'), b = text.value.split('\n')
    let start = 0, endA = a.length, endB = b.length
    while (start < Math.min(endA, endB) && a[start] === b[start]) start++
    while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) { endA--; endB-- }
    return { removed: a.slice(start, endA).join('\n'), added: b.slice(start, endB).join('\n'), line: start + 1 }
  })
  function mutate(fn) {
    if (parseError.value) throw new Error('请先修复 YAML 语法错误，再使用可视化编辑。')
    const doc = parseDocument(text.value)
    fn(doc)
    text.value = doc.toString({ indent: 2, lineWidth: 0 })
  }
  function pluginPath(tag) { return ['plugins', (config.value.plugins || []).findIndex(p => p.tag === tag)] }
  function insertPlugin(doc, value) {
    if (!doc.has('plugins')) doc.set('plugins', [])
    const items = doc.get('plugins').items
    let index = items.findIndex(p => p.get('type') === 'sequence')
    if (index < 0) index = items.length
    items.splice(index, 0, doc.createNode(value))
  }
  async function load(force = false) {
    if (loaded.value && !force) return
    busy.value = true
    try { const data = await getJSON('/api/v1/config'); text.value = original.value = data.text; digest.value = data.sha256; loaded.value = true }
    catch (e) { fail(e) } finally { busy.value = false }
  }
  async function format() {
    busy.value = true
    try { const data = await postJSON('/api/v1/config/format', { text: text.value }); text.value = data.text; notify('已格式化为两空格缩进，保留原有注释。') }
    catch (e) { fail(e) } finally { busy.value = false }
  }
  async function save(validateOnly = false, apply = false) {
    const submitted = text.value, submittedDigest = digest.value
    busy.value = true
    try {
      const data = await postJSON('/api/v1/config', { text: submitted, sha256: submittedDigest, validate_only: validateOnly, apply })
      if (validateOnly) { notify('配置和插件运行校验通过。'); return true }
      digest.value = data.sha256; original.value = submitted; review.value = false
      notify(apply ? '配置已保存，正在重启应用；页面会自动等待服务恢复。' : `已保存并备份：${data.backup}。点击“应用配置”使其生效。`)
      if (apply) applyState.value = { state: 'applying' }
      return true
    } catch (e) { fail(e); return false } finally { busy.value = false }
  }
  async function pollApply() {
    if (applyState.value?.state !== 'applying') return false
    try {
      const data = await getJSON('/api/v1/config/apply-status'); applyState.value = data
      if (['applied', 'rolled_back', 'failed'].includes(data.state)) {
        if (data.state === 'applied') notify(data.message); else fail(new Error(data.message))
        if (data.state === 'rolled_back') await load(true)
        return true
      }
    } catch { /* A service restart temporarily closes the HTTP listener. */ }
    return false
  }
  return { text, original, digest, busy, loaded, review, applyState, config, groups, providers, sequences, matchers, dirty, parseError, delta, mutate, pluginPath, insertPlugin, load, format, save, pollApply }
}
