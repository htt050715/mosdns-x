import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { fetchAuditCapacity, fetchAuditStatus, fetchDashboardStats } from '../services/dashboard'
import type { DashboardMetrics } from '../types/dashboard'

interface UseRealtimeMetricsOptions {
  pollIntervalMs?: number
  windowSize?: number
  listenGlobalRefresh?: boolean
}

const DEFAULT_POLL_INTERVAL_MS = 3000
const DEFAULT_WINDOW_SIZE = 40

function formatTimelineLabel(date: Date): string {
  return date.toLocaleTimeString('zh-CN', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
}

export function useRealtimeMetrics(options: UseRealtimeMetricsOptions = {}) {
  const pollIntervalMs = Math.max(1000, Number(options.pollIntervalMs || DEFAULT_POLL_INTERVAL_MS))
  const windowSize = Math.max(30, Number(options.windowSize || DEFAULT_WINDOW_SIZE))
  const listenGlobalRefresh = options.listenGlobalRefresh !== false

  const metrics = reactive<DashboardMetrics>({
    timestamps: [],
    requestCounts: [],
    avgLatencyMs: [],
    totalQueries: 0,
    averageLatency: 0,
    currentQueries: 0,
    currentLatency: 0
  })

  const isRunning = ref(true)
  const initialized = ref(false)
  const warningMessage = ref('')
  const lastUpdatedText = ref('--')

  const inFlight = ref(false)
  const auditCapacity = ref(0)
  let pollTimerId = 0
  let previousTotalQueries: number | null = null
  let previousDuration: number | null = null

  function appendPoint(timestamp: string, requestCount: number, avgLatencyMs: number) {
    metrics.timestamps.push(timestamp)
    metrics.requestCounts.push(requestCount)
    metrics.avgLatencyMs.push(avgLatencyMs)

    while (metrics.timestamps.length > windowSize) {
      metrics.timestamps.shift()
    }
    while (metrics.requestCounts.length > windowSize) {
      metrics.requestCounts.shift()
    }
    while (metrics.avgLatencyMs.length > windowSize) {
      metrics.avgLatencyMs.shift()
    }
  }

  async function refreshMetrics() {
    if (inFlight.value) {
      return
    }

    inFlight.value = true
    try {
      const [statsResult, statusResult, capacityResult] = await Promise.allSettled([
        fetchDashboardStats(),
        fetchAuditStatus(),
        auditCapacity.value > 0 ? Promise.resolve(auditCapacity.value) : fetchAuditCapacity()
      ])

      if (statusResult.status === 'fulfilled') {
        isRunning.value = statusResult.value
      }

      if (capacityResult.status === 'fulfilled') {
        auditCapacity.value = Number(capacityResult.value || 0)
      }

      if (statsResult.status !== 'fulfilled') {
        throw statsResult.reason
      }

      const stats = statsResult.value
      const totalQueries = Number(stats.totalQueries || 0)
      const averageLatency = Number(stats.averageLatency || 0)
      const fallbackCurrentQueries = previousTotalQueries === null
        ? 0
        : totalQueries >= previousTotalQueries
          ? totalQueries - previousTotalQueries
          : totalQueries

      const currentQueries = fallbackCurrentQueries
      const sameProcess = previousTotalQueries !== null && totalQueries >= previousTotalQueries
      const durationDelta = sameProcess && previousDuration !== null ? stats.totalDuration - previousDuration : stats.totalDuration
      const currentLatency = currentQueries > 0 ? Math.max(0, durationDelta) / currentQueries : 0
      previousTotalQueries = totalQueries
      previousDuration = stats.totalDuration

      metrics.totalQueries = totalQueries
      metrics.averageLatency = averageLatency
      metrics.currentQueries = currentQueries
      metrics.currentLatency = currentLatency

      appendPoint(formatTimelineLabel(new Date()), currentQueries, currentLatency)
      warningMessage.value = ''
      lastUpdatedText.value = new Date().toLocaleString('zh-CN', { hour12: false })
      initialized.value = true
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      warningMessage.value = `实时数据刷新失败: ${message}`
      console.error('[DnsOverviewCard] polling failed', error)
    } finally {
      inFlight.value = false
    }
  }

  function stopPolling() {
    if (pollTimerId) {
      window.clearInterval(pollTimerId)
      pollTimerId = 0
    }
  }

  function startPolling() {
    stopPolling()
    pollTimerId = window.setInterval(() => {
      void refreshMetrics()
    }, pollIntervalMs)
  }

  function handleGlobalRefresh() {
    void refreshMetrics()
  }

  onMounted(() => {
    void refreshMetrics()
    startPolling()
    if (listenGlobalRefresh) {
      window.addEventListener('mosdns-log-refresh', handleGlobalRefresh)
    }
  })

  onBeforeUnmount(() => {
    stopPolling()
    if (listenGlobalRefresh) {
      window.removeEventListener('mosdns-log-refresh', handleGlobalRefresh)
    }
  })

  return {
    metrics,
    isRunning,
    initialized,
    warningMessage,
    lastUpdatedText,
    refreshMetrics
  }
}
