<script lang="ts">
  import {
    Activity,
    Check,
    Filter,
    Radio,
    Search,
    Terminal,
    Trash2,
    Zap
  } from 'lucide-svelte'
  import { getAuthHeaders } from '../api/client'

  interface StreamEvent {
    id: string
    timestamp: string
    provider?: string
    model?: string
    promptTokens?: number
    completionTokens?: number
    cost?: number
    status?: string
    latency?: number
  }

  let events = $state<StreamEvent[]>([])
  let isConnected = $state(false)
  let filterLevel = $state<'ALL' | 'SUCCESS' | 'ERROR'>('ALL')
  let searchQuery = $state('')
  let autoScroll = $state(true)
  let terminalElement = $state<HTMLDivElement | null>(null)

  $effect(() => {
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
                const parsed: StreamEvent = JSON.parse(trimmed.slice(6))
                events = [parsed, ...events.slice(0, 99)]
                if (autoScroll && terminalElement) {
                  terminalElement.scrollTop = 0
                }
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

  let filteredEvents = $derived(
    events.filter((e) => {
      if (filterLevel === 'SUCCESS' && e.status && e.status !== 'success' && e.status !== '200') return false
      if (filterLevel === 'ERROR' && (!e.status || e.status === 'success' || e.status === '200')) return false

      if (searchQuery.trim()) {
        const query = searchQuery.toLowerCase()
        const match =
          (e.provider && e.provider.toLowerCase().includes(query)) ||
          (e.model && e.model.toLowerCase().includes(query)) ||
          (e.id && e.id.toLowerCase().includes(query))
        if (!match) return false
      }

      return true
    })
  )
</script>

<div class="p-4 sm:p-6 lg:p-8 max-w-[1560px] mx-auto space-y-6">
  <!-- Header -->
  <div class="flex flex-col md:flex-row md:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[11px] uppercase tracking-wider text-secondary px-2 py-0.5 rounded bg-secondary/10 border border-secondary/20 font-bold">
          Console & Diagnostics
        </span>
        <span class="text-outline">•</span>
        <span class="font-code text-[11px] {isConnected ? 'text-tertiary' : 'text-primary'} flex items-center gap-1.5">
          <span class="w-1.5 h-1.5 rounded-full {isConnected ? 'bg-tertiary animate-pulse' : 'bg-primary'}"></span>
          {isConnected ? 'Daemon Stream Connected' : 'Connecting to Core Stream...'}
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-on-surface tracking-tight">
        Live Console Logs & Terminal
      </h1>
      <p class="font-body text-xs sm:text-sm text-on-surface-variant max-w-2xl leading-relaxed">
        Real-time SSE event pipeline, token streaming metrics, upstream failover cascades, and socket lifecycles.
      </p>
    </div>

    <!-- Clear / Actions -->
    <div class="flex items-center gap-2">
      <button
        type="button"
        onclick={() => (events = [])}
        class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-surface-container hover:bg-surface-container-high text-on-surface-variant hover:text-on-surface font-body text-xs border border-surface-container-high transition cursor-pointer"
      >
        <Trash2 class="w-3.5 h-3.5" />
        <span>Clear Logs</span>
      </button>
    </div>
  </div>

  <!-- Terminal Frame (Mac-Style Window from Stitch) -->
  <div class="rounded-2xl bg-surface-container-lowest border border-surface-container-high shadow-2xl overflow-hidden flex flex-col h-[640px]">
    <!-- Terminal Title Bar -->
    <div class="h-10 bg-surface-container-low px-4 flex items-center justify-between border-b border-surface-container select-none">
      <div class="flex items-center gap-2">
        <span class="w-3 h-3 rounded-full bg-[#ff5f56]"></span>
        <span class="w-3 h-3 rounded-full bg-[#ffbd2e]"></span>
        <span class="w-3 h-3 rounded-full bg-[#27c93f]"></span>
        <span class="ml-2 font-code text-xs text-on-surface font-medium flex items-center gap-1.5">
          <Terminal class="w-3.5 h-3.5 text-secondary" />
          <span>daemon.log • 9router_core_v1.8.11</span>
        </span>
      </div>

      <div class="flex items-center gap-4 text-xs font-code">
        <span class="text-outline hidden sm:inline">UTF-8 • SSE Wire Protocol • /usage/stream</span>
        <span class="px-2 py-0.5 rounded bg-surface-container text-secondary">
          {filteredEvents.length} events
        </span>
      </div>
    </div>

    <!-- Filter & Search Toolbar inside Terminal -->
    <div class="p-2.5 bg-surface-container/60 border-b border-surface-container flex flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-2 flex-1 min-w-[200px] max-w-sm">
        <div class="relative w-full">
          <Search class="absolute left-2.5 top-2 w-3.5 h-3.5 text-outline pointer-events-none" />
          <input
            type="text"
            bind:value={searchQuery}
            placeholder="Search logs by provider, model, ID..."
            class="w-full bg-surface-container-lowest border border-surface-container-high rounded pl-8 pr-3 py-1 font-code text-xs text-on-surface placeholder:text-outline focus:outline-none focus:border-secondary transition"
          />
        </div>
      </div>

      <div class="flex items-center gap-1.5">
        <span class="text-[10px] font-code text-outline uppercase">Filter:</span>
        <button
          type="button"
          onclick={() => (filterLevel = 'ALL')}
          class="px-2.5 py-0.5 rounded font-code text-[11px] font-semibold transition cursor-pointer {filterLevel === 'ALL'
            ? 'bg-primary-container text-on-primary'
            : 'bg-surface-container-high text-on-surface-variant hover:text-on-surface'}"
        >
          ALL
        </button>
        <button
          type="button"
          onclick={() => (filterLevel = 'SUCCESS')}
          class="px-2.5 py-0.5 rounded font-code text-[11px] font-semibold transition cursor-pointer {filterLevel === 'SUCCESS'
            ? 'bg-tertiary/20 text-tertiary border border-tertiary/30'
            : 'bg-surface-container-high text-on-surface-variant hover:text-on-surface'}"
        >
          200 OK
        </button>
        <button
          type="button"
          onclick={() => (filterLevel = 'ERROR')}
          class="px-2.5 py-0.5 rounded font-code text-[11px] font-semibold transition cursor-pointer {filterLevel === 'ERROR'
            ? 'bg-error-container text-on-error-container'
            : 'bg-surface-container-high text-on-surface-variant hover:text-on-surface'}"
        >
          ERRORS
        </button>
      </div>

      <label class="flex items-center gap-2 cursor-pointer text-xs font-code text-on-surface-variant select-none">
        <input type="checkbox" bind:checked={autoScroll} class="rounded bg-surface-container-high border-surface-container" />
        <span>Auto-scroll</span>
      </label>
    </div>

    <!-- Terminal Output Canvas -->
    <div
      bind:this={terminalElement}
      class="flex-1 overflow-y-auto p-4 font-code text-xs leading-relaxed select-text space-y-1 bg-surface-container-lowest text-on-surface"
    >
      {#if filteredEvents.length > 0}
        {#each filteredEvents as evt (evt.id || Math.random())}
          {@const isSuccess = !evt.status || evt.status === 'success' || evt.status === '200'}
          <div class="flex flex-wrap items-start gap-2 py-1 px-2 rounded hover:bg-surface-container-low/70 transition border-b border-surface-container/20">
            <span class="text-outline text-[11px] flex-shrink-0">
              {evt.timestamp ? new Date(evt.timestamp).toLocaleTimeString() : 'now'}
            </span>

            <span class="px-1.5 py-0.2 rounded font-bold text-[10px] {isSuccess
              ? 'bg-tertiary/15 text-tertiary border border-tertiary/20'
              : 'bg-error/15 text-error border border-error/20'}">
              {evt.status || '200 OK'}
            </span>

            <span class="font-bold text-secondary flex-shrink-0">
              [{evt.provider || 'gateway'}]
            </span>

            <span class="text-on-surface flex-shrink-0">
              {evt.model || 'unknown-model'}
            </span>

            <div class="flex items-center gap-2 text-on-surface-variant ml-auto text-[11px]">
              {#if evt.promptTokens}
                <span>in: <strong class="text-primary">{evt.promptTokens}</strong>t</span>
              {/if}
              {#if evt.completionTokens}
                <span>out: <strong class="text-tertiary">{evt.completionTokens}</strong>t</span>
              {/if}
              {#if evt.latency}
                <span class="text-secondary">{evt.latency}ms</span>
              {/if}
            </div>
          </div>
        {/each}
      {:else}
        <div class="h-full flex flex-col items-center justify-center text-outline space-y-2">
          <Terminal class="w-8 h-8 text-outline/50" />
          <span>Waiting for streaming events from gateway...</span>
          <span class="text-[11px]">Run a prompt in Combos Live Test or connect a client to see logs.</span>
        </div>
      {/if}
    </div>
  </div>
</div>
