<script setup>
import { computed } from 'vue'
const props = defineProps({ steps: Array, path: Array, editor: Object, depth: { type: Number, default: 0 } })
const emit = defineEmits(['error', 'notice'])
const groups = computed(() => props.editor.groups.value)
const plugins = computed(() => props.editor.config.value.plugins || [])
const isGroup = tag => groups.value.some(g => g.tag === tag)
function label(tag) {
  const p = plugins.value.find(p => p.tag === tag)
  if (p?.type === 'query_matcher') return (p.args?.domain || []).join(' · ') || tag
  return p?.type || (tag?.startsWith('_') ? '内置操作' : tag)
}
function conditionHint(expr) {
  return plugins.value.filter(p => p.type === 'query_matcher' && (String(expr).match(/[A-Za-z_][\w-]*/g) || []).includes(p.tag)).map(p => `${p.tag}: ${(p.args?.domain || []).join(' · ') || JSON.stringify(p.args)}`).join(' | ')
}
function change(path, value) {
  try { props.editor.mutate(doc => doc.setIn(path, value)); emit('notice', '流量导向已更新到草稿，保存并应用后生效。') }
  catch (e) { emit('error', e) }
}
</script>
<template>
  <ol class="flow-list">
    <li v-for="(step, i) in steps" :key="i" class="flow-step">
      <span class="flow-order">{{ i + 1 }}</span>
      <div v-if="typeof step === 'string'" class="flow-operation" :class="{ destination: isGroup(step) }">
        <span class="flow-symbol">{{ isGroup(step) ? '↗' : '↓' }}</span>
        <select v-if="isGroup(step)" :value="step" :aria-label="`步骤 ${i + 1} 目标上游组`" @change="change([...path, i], $event.target.value)"><option v-for="g in groups" :key="g.tag" :value="g.tag">{{ g.tag }}</option></select>
        <strong v-else class="mono">{{ step }}</strong><span class="badge subtle">{{ label(step) }}</span>
      </div>
      <div v-else class="flow-branch">
        <div class="condition"><span class="badge">IF</span><input :value="step.if" aria-label="匹配条件" @change="change([...path, i, 'if'], $event.target.value)"></div>
        <small class="condition-hint">{{ conditionHint(step.if) || label(String(step.if)) }}</small>
        <div v-if="depth < 15" class="branch-path"><span class="branch-label">匹配时 →</span><FlowNode :steps="step.exec || []" :path="[...path, i, 'exec']" :editor="editor" :depth="depth + 1" @error="emit('error', $event)" @notice="emit('notice', $event)" /></div>
        <div v-if="step.else_exec?.length && depth < 15" class="branch-path otherwise"><span class="branch-label">否则 →</span><FlowNode :steps="step.else_exec" :path="[...path, i, 'else_exec']" :editor="editor" :depth="depth + 1" @error="emit('error', $event)" @notice="emit('notice', $event)" /></div>
      </div>
    </li>
  </ol>
</template>
