<script lang="ts">
  import { Activity, Clock, Cpu, Radio, Zap } from 'lucide-svelte'
  import { api, getAuthHeaders } from '../api/client'

  interface StreamEvent {
    id: string
    timestamp: string
    provider?: string
    model?: string
    promptTokens?: number
    completionTokens?: number
    cost?: number
    status?: string
  }

  let stats = $state<{
    promptTokens?: number
    completionTokens?: number
    totalTokens?: number
    requests?: number
  }>({})
  let events = $state<StreamEvent[]>([])
  let isConnected = $state(false)

  $effect(() => {
    api
      .getUsageStats()
      .then((data) => {
        if (data && typeof data === 'object') {
          stats = data as typeof stats
        }
      })
      .catch(() => {})

    let isCancelled = false

    const connectStream = async () => {
      try {
        const res = await fetch('/usage/stream', {
          headers: getAuthHeaders(),
        })

        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        isConnected = true

        const reader = res.body?.getReader()
        const decoder = new TextDecoder()
        if (!reader) return

        let buffer = ''
        while (!isCancelled) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            const trimmed = line.trim()
            if (!trimmed || trimmed.startsWith(':')) continue
            if (trimmed.startsWith('data: ')) {
              try {
                const parsed = JSON.parse(trimmed.slice(6))
                events = [parsed, ...events.slice(0, 49)]
              } catch {
                // ignore
              }
            }
          }
        }
      } catch {
        isConnected = false
        if (!isCancelled) setTimeout(connectStream, 5000)
      }
    }

    connectStream()

    return () => {
      isCancelled = true
    }
  })
</script>

<div class="p-6 max-w-7xl mx-auto space-y-6">
  <!-- Header -->
  <div class="flex items-center justify-between">
    <div>
      <h2 class="text-xl font-bold text-white tracking-tight flex items-center gap-2">
        <span>Live Analytics & Stream</span>
        <span
          class="flex items-center gap-1 text-[10px] font-bold px-2 py-0.5 rounded-full {isConnected
            ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
            : 'bg-amber-500/10 text-amber-400 border border-amber-500/20'}"
        >
          <Radio class="w-3 h-3 animate-pulse" />
          <span>{isConnected ? 'LIVE STREAM CONNECTED' : 'CONNECTING...'}</span>
        </span>
      </h2>
      <p class="text-xs text-slate-400">Real-time token metrics, throughput, and upstream SSE event logs</p>
    </div>
  </div>

  <!-- Metric Cards -->
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
    <div class="p-5 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
      <div class="flex items-center justify-between text-slate-400 text-xs">
        <span>Prompt Tokens</span>
        <Cpu class="w-4 h-4 text-indigo-400" />
      </div>
      <div class="text-2xl font-bold text-white font-mono">
        {stats.promptTokens?.toLocaleString() || '—'}
      </div>
    </div>

    <div class="p-5 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
      <div class="flex items-center justify-between text-slate-400 text-xs">
        <span>Completion Tokens</span>
        <Zap class="w-4 h-4 text-emerald-400" />
      </div>
      <div class="text-2xl font-bold text-white font-mono">
        {stats.completionTokens?.toLocaleString() || '—'}
      </div>
    </div>

    <div class="p-5 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
      <div class="flex items-center justify-between text-slate-400 text-xs">
        <span>Total Tokens</span>
        <Activity class="w-4 h-4 text-purple-400" />
      </div>
      <div class="text-2xl font-bold text-white font-mono">
        {stats.totalTokens?.toLocaleString() || '—'}
      </div>
    </div>

    <div class="p-5 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
      <div class="flex items-center justify-between text-slate-400 text-xs">
        <span>Total Requests</span>
        <Clock class="w-4 h-4 text-amber-400" />
      </div>
      <div class="text-2xl font-bold text-white font-mono">
        {stats.requests?.toLocaleString() || '—'}
      </div>
    </div>
  </div>

  <!-- Live Stream Table -->
  <div class="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
    <div class="flex items-center justify-between">
      <h3 class="text-sm font-bold text-white flex items-center gap-2">
        <Radio class="w-4 h-4 text-emerald-400" />
        <span>Live Stream Activity Feed</span>
      </h3>
      <span class="text-xs text-slate-500 font-mono">Auto-scrolling stream events</span>
    </div>

    <div class="overflow-x-auto">
      <table class="w-full text-left text-xs">
        <thead>
          <tr class="border-b border-slate-800 text-slate-400 font-medium">
            <th class="py-2.5 px-3">Time</th>
            <th class="py-2.5 px-3">Provider</th>
            <th class="py-2.5 px-3">Model</th>
            <th class="py-2.5 px-3">Prompt</th>
            <th class="py-2.5 px-3">Completion</th>
            <th class="py-2.5 px-3">Status</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-slate-800/50 font-mono">
          {#if events.length > 0}
            {#each events as evt, i (evt.id || i)}
              <tr class="hover:bg-slate-800/30 transition">
                <td class="py-2.5 px-3 text-slate-400">
                  {evt.timestamp ? new Date(evt.timestamp).toLocaleTimeString() : 'Just now'}
                </td>
                <td class="py-2.5 px-3 font-semibold text-indigo-400">{evt.provider || '—'}</td>
                <td class="py-2.5 px-3 text-slate-200">{evt.model || '—'}</td>
                <td class="py-2.5 px-3 text-slate-400">{evt.promptTokens || 0}</td>
                <td class="py-2.5 px-3 text-slate-400">{evt.completionTokens || 0}</td>
                <td class="py-2.5 px-3">
                  <span
                    class="px-2 py-0.5 rounded text-[10px] font-bold {evt.status === 'success' || !evt.status
                      ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                      : 'bg-rose-500/10 text-rose-400 border border-rose-500/20'}"
                  >
                    {evt.status || 'success'}
                  </span>
                </td>
              </tr>
            {/each}
          {:else}
            <tr>
              <td colspan="6" class="py-12 text-center text-slate-500">
                Listening for incoming streaming requests...
              </td>
            </tr>
          {/if}
        </tbody>
      </table>
    </div>
  </div>
</div>
