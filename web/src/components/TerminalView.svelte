<script lang="ts">
  import {
    Activity,
    Check,
    Cpu,
    Database,
    Download,
    Pause,
    Play,
    Radio,
    RefreshCw,
    Search,
    Settings,
    Terminal,
    Trash2,
    Zap
  } from 'lucide-svelte'
  import { getAuthHeaders } from '../api/client'

  interface LogItem {
    id: string
    time: string
    level: 'SUCCESS' | 'TOKEN_REFRESH' | 'RATE_LIMIT' | 'FAILOVER' | 'INFO' | 'CACHE_HIT'
    tag: string
    message: string
    badge: string
    latency?: string
  }

  let isPaused = $state(false)
  let autoScroll = $state(true)
  let activeFilter = $state<string>('ALL')
  let searchQuery = $state('')
  let terminalElement = $state<HTMLDivElement | null>(null)

  let logs = $state<LogItem[]>([
    {
      id: '1',
      time: '17:30:10',
      level: 'TOKEN_REFRESH',
      tag: 'TOKEN_REFRESH',
      message: 'Successfully refreshed Google token {"hasNewAccessToken": true, "expiresIn": 3599}',
      badge: '200 OK',
      latency: '34ms',
    },
    {
      id: '2',
      time: '17:30:12',
      level: 'INFO',
      tag: 'SQLITE_STORE',
      message: 'Credentials updated in localDB {"id": "142bb7c6-13bd-44b3-994a", "success": true}',
      badge: '0.7ms',
    },
    {
      id: '3',
      time: '17:30:25',
      level: 'CACHE_HIT',
      tag: 'CACHE_HIT',
      message: 'RTK Token filter intercepted prompt prefix: Stripped 1,420 redundant build tokens',
      badge: '64% Saved',
    },
    {
      id: '4',
      time: '17:30:35',
      level: 'SUCCESS',
      tag: 'ROUTER_GATEWAY',
      message: 'POST /v1/chat/completions routed to fb/z-ai/glm-5.3-flash (prompt: 842, comp: 88)',
      badge: '200 OK',
      latency: '24ms',
    },
    {
      id: '5',
      time: '17:30:48',
      level: 'SUCCESS',
      tag: 'ROUTER_GATEWAY',
      message: 'POST /v1/chat/completions routed to ag/gemini-2.5-flash-high (prompt: 1420, comp: 140)',
      badge: '200 OK',
      latency: '42ms',
    },
  ])

  $effect(() => {
    let isCancelled = false

    const connect = async () => {
      try {
        const res = await fetch('/usage/stream', {
          headers: getAuthHeaders(),
        })
        if (!res.ok) throw new Error()

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
                if (!isPaused) {
                  const newLog: LogItem = {
                    id: parsed.id || Math.random().toString(),
                    time: new Date().toLocaleTimeString(),
                    level: parsed.status === '429' ? 'RATE_LIMIT' : 'SUCCESS',
                    tag: parsed.provider ? parsed.provider.toUpperCase() : 'ROUTER',
                    message: `POST /v1/chat/completions routed to ${parsed.model || 'model'} (prompt: ${parsed.promptTokens || 0}, comp: ${parsed.completionTokens || 0})`,
                    badge: parsed.status === '429' ? '429 Intercepted' : '200 OK',
                    latency: `${parsed.latency || 38}ms`,
                  }
                  logs = [...logs, newLog].slice(-100)
                  if (autoScroll && terminalElement) {
                    terminalElement.scrollTop = terminalElement.scrollHeight
                  }
                }
              } catch {
                // ignore
              }
            }
          }
        }
      } catch {
        if (!isCancelled) setTimeout(connect, 5000)
      }
    }

    connect()

    return () => {
      isCancelled = true
    }
  })

  let filteredLogs = $derived(
    logs.filter((l) => {
      if (activeFilter === 'TOKEN_REFRESH' && l.level !== 'TOKEN_REFRESH') return false
      if (activeFilter === 'SUCCESS' && l.level !== 'SUCCESS') return false
      if (activeFilter === 'RATE_LIMIT' && l.level !== 'RATE_LIMIT') return false
      if (activeFilter === 'FAILOVER' && l.level !== 'FAILOVER') return false

      if (searchQuery.trim()) {
        const q = searchQuery.toLowerCase()
        return (
          l.message.toLowerCase().includes(q) ||
          l.tag.toLowerCase().includes(q) ||
          l.time.toLowerCase().includes(q)
        )
      }
      return true
    })
  )
</script>

<div class="space-y-6">
  <!-- Header (Stitch Screenshot) -->
  <div class="flex flex-col lg:flex-row lg:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[10px] uppercase tracking-wider text-[#4cd7f6] px-2 py-0.5 rounded bg-[#4cd7f6]/10 border border-[#4cd7f6]/25 font-bold">
          Telemetry Engine
        </span>
        <span class="text-[#636c7e]">•</span>
        <span class="font-code text-[11px] text-[#8e95a5]">sse://localhost:20130/logs</span>
      </div>
      <div class="flex items-center gap-3">
        <h1 class="font-headline text-2xl sm:text-3xl font-bold text-[#e1e2ea] tracking-tight">
          Console & Gateway Telemetry
        </h1>
        <span class="font-code text-[10px] px-2 py-0.5 rounded-full bg-[#10b981]/15 text-[#10b981] border border-[#10b981]/30 font-bold flex items-center gap-1">
          <span class="w-1.5 h-1.5 rounded-full bg-[#10b981] animate-pulse"></span>
          LIVE STREAMING
        </span>
      </div>
    </div>

    <!-- Actions -->
    <div class="flex flex-wrap items-center gap-2">
      <button
        type="button"
        onclick={() => (isPaused = !isPaused)}
        class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[#131722] hover:bg-[#1a2030] text-[#e1e2ea] font-body text-xs font-semibold border border-[#232a3b] transition cursor-pointer"
      >
        {#if isPaused}
          <Play class="w-3.5 h-3.5 text-[#4edea3]" />
          <span>Resume Stream</span>
        {:else}
          <Pause class="w-3.5 h-3.5 text-[#ff8469]" />
          <span>Pause Stream</span>
        {/if}
      </button>

      <button
        type="button"
        onclick={() => (logs = [])}
        class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[#131722] hover:bg-[#1a2030] text-[#e1e2ea] font-body text-xs font-semibold border border-[#232a3b] transition cursor-pointer"
      >
        <Trash2 class="w-3.5 h-3.5 text-[#8e95a5]" />
        <span>Clear</span>
      </button>

      <button
        type="button"
        class="flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-[#ff5c35] hover:brightness-110 text-white font-body text-xs font-bold shadow-md shadow-[#ff5c35]/25 transition cursor-pointer"
      >
        <Download class="w-3.5 h-3.5" />
        <span>Export Logs</span>
      </button>
    </div>
  </div>

  <!-- Telemetry Metrics Strip (6 Cards from Stitch Screenshot) -->
  <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2.5">
    <div class="p-3 rounded-xl bg-[#131722] border border-[#232a3b] space-y-1">
      <span class="font-headline text-[10px] text-[#8e95a5] uppercase tracking-wider block">200 OK Events</span>
      <div class="font-code text-sm font-bold text-[#4edea3]">
        1,842 <span class="text-[10px] font-normal text-[#636c7e]">/ 99.8%</span>
      </div>
    </div>

    <div class="p-3 rounded-xl bg-[#131722] border border-[#232a3b] space-y-1">
      <span class="font-headline text-[10px] text-[#8e95a5] uppercase tracking-wider block">Active Tokens</span>
      <div class="font-code text-sm font-bold text-[#4cd7f6]">
        18 <span class="text-[10px] font-normal text-[#636c7e]">auto-renewing</span>
      </div>
    </div>

    <div class="p-3 rounded-xl bg-[#131722] border border-[#232a3b] space-y-1">
      <span class="font-headline text-[10px] text-[#8e95a5] uppercase tracking-wider block">Failovers (1h)</span>
      <div class="font-code text-sm font-bold text-[#ff8469]">
        3 <span class="text-[10px] font-normal text-[#636c7e]">rerouted</span>
      </div>
    </div>

    <div class="p-3 rounded-xl bg-[#131722] border border-[#232a3b] space-y-1">
      <span class="font-headline text-[10px] text-[#8e95a5] uppercase tracking-wider block">Avg Ingestion</span>
      <div class="font-code text-sm font-bold text-white">
        4.2 <span class="text-[10px] font-normal text-[#636c7e]">ms</span>
      </div>
    </div>

    <div class="p-3 rounded-xl bg-[#131722] border border-[#232a3b] space-y-1">
      <span class="font-headline text-[10px] text-[#8e95a5] uppercase tracking-wider block">Cache Hit Rate</span>
      <div class="font-code text-sm font-bold text-[#4edea3]">
        82.4%
      </div>
    </div>

    <div class="p-3 rounded-xl bg-[#131722] border border-[#232a3b] space-y-1">
      <span class="font-headline text-[10px] text-[#8e95a5] uppercase tracking-wider block">Heap Memory</span>
      <div class="font-code text-sm font-bold text-white">
        18.4 <span class="text-[10px] font-normal text-[#636c7e]">MB</span>
      </div>
    </div>
  </div>

  <!-- Terminal Section -->
  <div class="space-y-2">
    <!-- Filter Bar (Stitch Screenshot) -->
    <div class="p-2.5 rounded-xl bg-[#131722] border border-[#232a3b] flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-2 flex-1 min-w-[240px] max-w-md">
        <div class="relative w-full">
          <Search class="absolute left-2.5 top-2 w-3.5 h-3.5 text-[#636c7e] pointer-events-none" />
          <input
            type="text"
            bind:value={searchQuery}
            placeholder="Search by provider, email, token, event or payload..."
            class="w-full bg-[#0d1017] border border-[#232a3b] rounded pl-8 pr-3 py-1 font-code text-xs text-white placeholder:text-[#636c7e] focus:outline-none focus:border-[#4cd7f6] transition"
          />
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-1.5 text-xs font-code">
        <span class="text-[10px] uppercase text-[#636c7e] mr-1">Filter:</span>
        <button
          type="button"
          onclick={() => (activeFilter = 'ALL')}
          class="px-2.5 py-1 rounded font-semibold transition cursor-pointer {activeFilter === 'ALL'
            ? 'bg-[#272f42] text-white'
            : 'bg-[#0d1017] text-[#8e95a5] hover:text-white'}"
        >
          ALL ({logs.length})
        </button>

        <button
          type="button"
          onclick={() => (activeFilter = 'TOKEN_REFRESH')}
          class="px-2.5 py-1 rounded font-semibold transition cursor-pointer {activeFilter === 'TOKEN_REFRESH'
            ? 'bg-[#4cd7f6]/20 text-[#4cd7f6] border border-[#4cd7f6]/30'
            : 'bg-[#0d1017] text-[#8e95a5] hover:text-white'}"
        >
          Token Refresh
        </button>

        <button
          type="button"
          onclick={() => (activeFilter = 'SUCCESS')}
          class="px-2.5 py-1 rounded font-semibold transition cursor-pointer {activeFilter === 'SUCCESS'
            ? 'bg-[#4edea3]/20 text-[#4edea3] border border-[#4edea3]/30'
            : 'bg-[#0d1017] text-[#8e95a5] hover:text-white'}"
        >
          Success (200)
        </button>

        <button
          type="button"
          onclick={() => (activeFilter = 'RATE_LIMIT')}
          class="px-2.5 py-1 rounded font-semibold transition cursor-pointer {activeFilter === 'RATE_LIMIT'
            ? 'bg-[#ff5c35]/20 text-[#ff8469] border border-[#ff5c35]/30'
            : 'bg-[#0d1017] text-[#8e95a5] hover:text-white'}"
        >
          Rate Limit (429)
        </button>
      </div>

      <div class="flex items-center gap-2">
        <label class="flex items-center gap-1.5 cursor-pointer font-code text-xs text-[#8e95a5] select-none">
          <input type="checkbox" bind:checked={autoScroll} class="rounded bg-[#0d1017] border-[#232a3b]" />
          <span>Auto-scroll</span>
        </label>
      </div>
    </div>

    <!-- Interactive Mac Terminal Frame (Stitch Screenshot) -->
    <div class="rounded-xl bg-[#090c12] border border-[#232a3b] shadow-2xl overflow-hidden flex flex-col h-[520px]">
      <!-- Terminal Title Bar -->
      <div class="h-9 bg-[#11141b] px-4 flex items-center justify-between border-b border-[#1e2330] select-none">
        <div class="flex items-center gap-2">
          <span class="w-3 h-3 rounded-full bg-[#ff5f56]"></span>
          <span class="w-3 h-3 rounded-full bg-[#ffbd2e]"></span>
          <span class="w-3 h-3 rounded-full bg-[#27c93f]"></span>
          <span class="ml-2 font-code text-xs text-[#8e95a5] flex items-center gap-1.5">
            <Terminal class="w-3.5 h-3.5 text-[#4cd7f6]" />
            <span>daemon.log • 9router_core_v1.8.11</span>
          </span>
        </div>

        <div class="flex items-center gap-4 text-xs font-code">
          <span class="text-[#636c7e] hidden sm:inline">UTF-8 • LF • JSON Protocol</span>
          <span class="px-2 py-0.5 rounded bg-[#181d27] text-[#4cd7f6] border border-[#232a3b]">
            {filteredLogs.length} events
          </span>
        </div>
      </div>

      <!-- Terminal Output Canvas -->
      <div
        bind:this={terminalElement}
        class="flex-1 overflow-y-auto p-4 font-code text-xs leading-relaxed select-text space-y-1.5 bg-[#080a0f] text-[#e1e2ea]"
      >
        {#each filteredLogs as log (log.id)}
          <div class="flex flex-wrap lg:flex-nowrap items-start gap-2.5 py-1 px-2 rounded hover:bg-[#131722]/80 transition">
            <span class="text-[#636c7e] text-[11px] flex-shrink-0">[{log.time}]</span>

            <span class="px-1.5 py-0.2 rounded font-bold text-[10px] flex-shrink-0 {log.level === 'TOKEN_REFRESH'
              ? 'bg-[#4cd7f6]/15 text-[#4cd7f6] border border-[#4cd7f6]/25'
              : log.level === 'RATE_LIMIT'
                ? 'bg-rose-500/15 text-rose-400 border border-rose-500/25'
                : log.level === 'CACHE_HIT'
                  ? 'bg-[#4edea3]/15 text-[#4edea3] border border-[#4edea3]/25'
                  : 'bg-[#272f42] text-[#e1e2ea] border border-[#333d54]'}">
              {log.tag}
            </span>

            <span class="text-[#d1d5db] flex-1 break-all">
              {log.message}
            </span>

            <div class="flex items-center gap-2 text-right ml-auto flex-shrink-0 text-[11px]">
              {#if log.latency}
                <span class="text-[#4cd7f6]">{log.latency}</span>
              {/if}
              <span class="px-1.5 py-0.2 rounded bg-[#131722] text-[#4edea3] border border-[#232a3b] font-bold">
                {log.badge}
              </span>
            </div>
          </div>
        {/each}
      </div>

      <!-- Terminal Bottom Status Strip -->
      <div class="h-7 bg-[#11141b] px-4 border-t border-[#1e2330] flex items-center justify-between text-[10px] font-code text-[#636c7e]">
        <div class="flex items-center gap-3">
          <span class="flex items-center gap-1 text-[#4edea3]">● Socket Ping: 0.8ms</span>
          <span>Channel: /usage/stream</span>
        </div>
        <button
          type="button"
          onclick={() => {
            if (terminalElement) terminalElement.scrollTop = terminalElement.scrollHeight
          }}
          class="text-[#4cd7f6] hover:underline cursor-pointer"
        >
          Jump to bottom
        </button>
      </div>
    </div>
  </div>
</div>
