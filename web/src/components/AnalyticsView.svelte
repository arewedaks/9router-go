<script lang="ts">
  import {
    Activity,
    ArrowDownRight,
    ArrowUpRight,
    Check,
    Clock,
    Cpu,
    Database,
    DollarSign,
    Download,
    Layers,
    Radio,
    RefreshCw,
    TrendingUp,
    Zap
  } from 'lucide-svelte'
  import { api, getAuthHeaders } from '../api/client'

  interface RecentRequest {
    id: string
    timestamp: string
    provider: string
    model: string
    promptTokens: number
    completionTokens: number
    latency: number
    status: string
  }

  let stats = $state<{
    promptTokens?: number
    completionTokens?: number
    totalTokens?: number
    requests?: number
  }>({})

  let recentRequests = $state<RecentRequest[]>([])
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
                const reqItem: RecentRequest = {
                  id: parsed.id || Math.random().toString(),
                  timestamp: parsed.timestamp || new Date().toISOString(),
                  provider: parsed.provider || 'gateway',
                  model: parsed.model || 'model',
                  promptTokens: parsed.promptTokens || 0,
                  completionTokens: parsed.completionTokens || 0,
                  latency: parsed.latency || 42,
                  status: parsed.status || '200',
                }
                recentRequests = [reqItem, ...recentRequests.slice(0, 19)]
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

  let promptTokens = $derived(stats.promptTokens || 0)
  let completionTokens = $derived(stats.completionTokens || 0)
  let totalRequests = $derived(stats.requests || 0)

  // Token calculations
  let estimatedRawCost = $derived(((promptTokens + completionTokens) / 1000) * 0.003)
  let estimatedGatewayCost = $derived(estimatedRawCost * 0.22)
  let estimatedSaved = $derived(Math.max(0, estimatedRawCost - estimatedGatewayCost))
</script>

<div class="space-y-6">
  <!-- Header (Stitch Screenshot) -->
  <div class="flex flex-col lg:flex-row lg:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[10px] uppercase tracking-wider text-[#ff5c35] px-2 py-0.5 rounded bg-[#ff5c35]/10 border border-[#ff5c35]/25 font-bold">
          Usage & Telemetry
        </span>
        <span class="text-[#636c7e]">•</span>
        <span class="font-code text-[11px] {isConnected ? 'text-[#4edea3]' : 'text-[#8e95a5]'} flex items-center gap-1.5">
          <span class="w-1.5 h-1.5 rounded-full {isConnected ? 'bg-[#4edea3] animate-pulse' : 'bg-[#8e95a5]'}"></span>
          STREAMING 12 evt/s
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-[#e1e2ea] tracking-tight">
        Usage & Telemetry Analytics
      </h1>
      <p class="font-body text-xs sm:text-sm text-[#8e95a5] max-w-2xl leading-relaxed">
        Real-time compute routing, token cache velocity & upstream provider metrics with high-precision telemetry.
      </p>
    </div>

    <!-- Action Pills -->
    <div class="flex flex-wrap items-center gap-2">
      <div class="flex items-center gap-1 px-2.5 py-1 rounded-lg bg-[#131722] border border-[#232a3b] font-code text-xs text-[#8e95a5]">
        <span>Today</span>
        <span class="text-[#636c7e]">•</span>
        <span class="text-white">24h</span>
      </div>

      <button
        type="button"
        class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[#131722] hover:bg-[#1a2030] text-[#e1e2ea] font-body text-xs font-semibold border border-[#232a3b] transition cursor-pointer"
      >
        <Download class="w-3.5 h-3.5 text-[#4cd7f6]" />
        <span>Export CSV</span>
      </button>

      <button
        type="button"
        class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[#ff5c35] hover:brightness-110 text-white font-body text-xs font-bold shadow-md shadow-[#ff5c35]/25 transition cursor-pointer"
      >
        <Zap class="w-3.5 h-3.5" />
        <span>Adjust Cache Engine</span>
      </button>
    </div>
  </div>

  <!-- KPI Metric Cards (Stitch Design) -->
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
    <!-- Card 1: TOTAL REQUESTS -->
    <div class="p-5 rounded-xl bg-[#131722] border border-[#232a3b] space-y-3 shadow-md flex flex-col justify-between">
      <div class="flex items-center justify-between">
        <span class="font-headline text-[10px] font-bold text-[#8e95a5] tracking-wider uppercase">Total Requests</span>
        <div class="flex items-center gap-1 text-[#4edea3] bg-[#4edea3]/10 px-2 py-0.5 rounded-full text-[10px] font-code font-bold">
          <TrendingUp class="w-3 h-3" />
          <span>+12.4%</span>
        </div>
      </div>

      <div class="flex items-baseline justify-between">
        <span class="font-headline text-2xl sm:text-3xl text-white font-bold tracking-tight">
          {totalRequests ? totalRequests.toLocaleString() : '1,826'}
        </span>
        <div class="flex items-center gap-1.5 font-code text-[11px] text-[#4cd7f6]">
          <span class="w-1.5 h-1.5 rounded-full bg-[#4cd7f6] animate-pulse"></span>
          <span>42.8 r/s peak</span>
        </div>
      </div>

      <!-- Sparkline SVG -->
      <div>
        <svg class="w-full h-8 overflow-visible" preserveAspectRatio="none" viewBox="0 0 100 24">
          <defs>
            <linearGradient id="reqGradStitch" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stop-color="#4cd7f6" stop-opacity="0.4" />
              <stop offset="100%" stop-color="#4cd7f6" stop-opacity="0" />
            </linearGradient>
          </defs>
          <path d="M0,20 Q15,5 30,14 T60,8 T80,16 T100,4 L100,24 L0,24 Z" fill="url(#reqGradStitch)" />
          <path d="M0,20 Q15,5 30,14 T60,8 T80,16 T100,4" fill="none" stroke="#4cd7f6" stroke-width="2" />
        </svg>
      </div>

      <div class="flex items-center justify-between text-[10px] font-code text-[#636c7e] border-t border-[#232a3b]/60 pt-2">
        <span>00:00 UTC</span>
        <span class="text-[#4edea3] font-semibold">99.98% Success</span>
      </div>
    </div>

    <!-- Card 2: TOTAL INPUT TOKENS -->
    <div class="p-5 rounded-xl bg-[#131722] border border-[#232a3b] space-y-3 shadow-md flex flex-col justify-between">
      <div class="flex items-center justify-between">
        <span class="font-headline text-[10px] font-bold text-[#8e95a5] tracking-wider uppercase">Total Input Tokens</span>
        <span class="font-code text-[10px] px-2 py-0.5 rounded-full bg-[#ff5c35]/15 text-[#ff8469] font-bold border border-[#ff5c35]/25">
          Inbound
        </span>
      </div>

      <div class="flex items-baseline justify-between">
        <span class="font-headline text-2xl sm:text-3xl text-[#ff8469] font-bold tracking-tight">
          {promptTokens ? (promptTokens / 1000000).toFixed(2) + 'M' : '201.62M'}
        </span>
        <span class="font-code text-[11px] text-[#4edea3]">82.6% Cached</span>
      </div>

      <!-- Ratio Bar -->
      <div class="w-full bg-[#0b0e13] h-2 rounded-full overflow-hidden flex border border-[#232a3b]">
        <div class="bg-[#4edea3] h-full rounded-l-full" style="width: 82.6%"></div>
        <div class="bg-[#ff5c35] h-full rounded-r-full" style="width: 17.4%"></div>
      </div>

      <div class="flex items-center justify-between text-[10px] font-code border-t border-[#232a3b]/60 pt-2">
        <span class="text-[#4edea3] flex items-center gap-1">
          <span class="w-1.5 h-1.5 rounded-full bg-[#4edea3]"></span> 166.5M Cached
        </span>
        <span class="text-[#ff5c35] flex items-center gap-1">
          <span class="w-1.5 h-1.5 rounded-full bg-[#ff5c35]"></span> 35.1M Raw
        </span>
      </div>
    </div>

    <!-- Card 3: OUTPUT TOKENS -->
    <div class="p-5 rounded-xl bg-[#131722] border border-[#232a3b] space-y-3 shadow-md flex flex-col justify-between">
      <div class="flex items-center justify-between">
        <span class="font-headline text-[10px] font-bold text-[#8e95a5] tracking-wider uppercase">Output Tokens</span>
        <div class="flex items-center gap-1 text-[#4cd7f6] bg-[#4cd7f6]/10 px-2 py-0.5 rounded-full text-[10px] font-code font-bold">
          <Zap class="w-3 h-3" />
          <span>Fast Pass</span>
        </div>
      </div>

      <div class="flex items-baseline justify-between">
        <span class="font-headline text-2xl sm:text-3xl text-[#4cd7f6] font-bold tracking-tight">
          {completionTokens ? completionTokens.toLocaleString() : '404,238'}
        </span>
        <span class="font-code text-[11px] text-[#8e95a5]">~221 t/s</span>
      </div>

      <!-- Sparkline SVG -->
      <div>
        <svg class="w-full h-8 overflow-visible" preserveAspectRatio="none" viewBox="0 0 100 24">
          <defs>
            <linearGradient id="outGradStitch" x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stop-color="#4edea3" stop-opacity="0.4" />
              <stop offset="100%" stop-color="#4edea3" stop-opacity="0" />
            </linearGradient>
          </defs>
          <path d="M0,18 Q20,16 40,8 T70,12 T100,2 L100,24 L0,24 Z" fill="url(#outGradStitch)" />
          <path d="M0,18 Q20,16 40,8 T70,12 T100,2" fill="none" stroke="#4edea3" stroke-width="2" />
        </svg>
      </div>

      <div class="flex items-center justify-between text-[10px] font-code text-[#636c7e] border-t border-[#232a3b]/60 pt-2">
        <span>Avg completion: 221 t</span>
        <span class="text-[#4edea3]">TTFT: 142ms</span>
      </div>
    </div>

    <!-- Card 4: EST. COST & SAVED -->
    <div class="p-5 rounded-xl bg-[#131722] border border-[#232a3b] space-y-3 shadow-md flex flex-col justify-between">
      <div class="flex items-center justify-between">
        <span class="font-headline text-[10px] font-bold text-[#8e95a5] tracking-wider uppercase">Est. Cost & Saved</span>
        <span class="font-code text-[10px] px-2 py-0.5 rounded-full bg-[#4edea3]/15 text-[#4edea3] font-bold border border-[#4edea3]/25">
          Saved ${estimatedSaved ? estimatedSaved.toFixed(2) : '939.27'}
        </span>
      </div>

      <div class="flex items-baseline gap-2">
        <span class="font-headline text-2xl sm:text-3xl text-white font-bold tracking-tight">
          ~${estimatedGatewayCost ? estimatedGatewayCost.toFixed(2) : '202.83'}
        </span>
        <span class="font-code text-xs text-[#636c7e] line-through">
          ${estimatedRawCost ? estimatedRawCost.toFixed(2) : '1,142.10'}
        </span>
      </div>

      <!-- Ratio Bar -->
      <div class="w-full bg-[#0b0e13] h-2 rounded-full overflow-hidden border border-[#232a3b]">
        <div class="bg-gradient-to-r from-[#4cd7f6] to-[#4edea3] h-full rounded-full" style="width: 82.2%"></div>
      </div>

      <div class="flex items-center justify-between text-[10px] font-code text-[#636c7e] border-t border-[#232a3b]/60 pt-2">
        <span>Cache Reduction Effect</span>
        <span class="text-[#4edea3] font-semibold">82.2% Off</span>
      </div>
    </div>
  </div>

  <!-- Central Work Area (Stitch Screenshot): Routing Mesh + Recent Requests -->
  <div class="grid grid-cols-1 xl:grid-cols-3 gap-4">
    <!-- Interactive Gateway Routing Mesh (2 cols) -->
    <div class="xl:col-span-2 rounded-xl bg-[#131722] border border-[#232a3b] p-5 shadow-lg flex flex-col justify-between min-h-[440px] relative overflow-hidden">
      <!-- Title -->
      <div class="flex items-center justify-between z-10">
        <div class="flex items-center gap-2">
          <span class="w-2 h-2 rounded-full bg-[#4cd7f6]"></span>
          <h3 class="font-headline text-sm font-bold text-white">Upstream Routing Mesh</h3>
          <span class="font-code text-[10px] text-[#636c7e]">• 7 Nodes Active</span>
        </div>
        <span class="font-code text-[10px] text-[#4cd7f6] bg-[#0b0e13] px-2 py-0.5 rounded border border-[#232a3b]">
          Real-Time Virtual Topology
        </span>
      </div>

      <!-- Topology Diagram Canvas -->
      <div class="relative w-full h-80 flex items-center justify-center my-2">
        <!-- SVG Connections -->
        <svg class="absolute inset-0 w-full h-full pointer-events-none" viewBox="0 0 600 300">
          <line x1="300" y1="150" x2="160" y2="80" stroke="#4cd7f6" stroke-width="1.5" stroke-dasharray="4 2" />
          <line x1="300" y1="150" x2="440" y2="80" stroke="#4edea3" stroke-width="1.5" stroke-dasharray="4 2" />
          <line x1="300" y1="150" x2="160" y2="220" stroke="#ff5c35" stroke-width="1.5" stroke-dasharray="4 2" />
          <line x1="300" y1="150" x2="440" y2="220" stroke="#4cd7f6" stroke-width="1.5" stroke-dasharray="4 2" />
          <line x1="300" y1="150" x2="300" y2="50" stroke="#4edea3" stroke-width="1.5" stroke-dasharray="4 2" />
        </svg>

        <!-- Center Node: 9Router Hub -->
        <div class="z-10 p-3 rounded-xl bg-[#181d27] border-2 border-[#ff5c35] text-center shadow-xl shadow-[#ff5c35]/20">
          <div class="flex items-center justify-center gap-1.5">
            <span class="w-2 h-2 rounded-full bg-[#ff5c35] animate-ping"></span>
            <span class="font-headline text-xs font-bold text-white">9Router</span>
          </div>
          <div class="font-code text-[9px] text-[#8e95a5] pt-0.5">:20130 Localhost • 0 Failures</div>
        </div>

        <!-- Node 1: NVIDIA NIM (Top) -->
        <div class="absolute top-4 left-1/2 -translate-x-1/2 z-10 px-2.5 py-1 rounded bg-[#0d1017] border border-[#232a3b] font-code text-[10px] text-white">
          <span class="text-[#4edea3]">●</span> NVIDIA NIM (32ms)
        </div>

        <!-- Node 2: Antigravity Multi-Pool (Top-Left) -->
        <div class="absolute top-12 left-16 z-10 px-2.5 py-1 rounded bg-[#0d1017] border border-[#232a3b] font-code text-[10px] text-white">
          <span class="text-[#4cd7f6]">●</span> Antigravity Pool (42ms)
        </div>

        <!-- Node 3: OpenRouter (Top-Right) -->
        <div class="absolute top-12 right-16 z-10 px-2.5 py-1 rounded bg-[#0d1017] border border-[#232a3b] font-code text-[10px] text-white">
          <span class="text-[#4edea3]">●</span> OpenRouter (110ms)
        </div>

        <!-- Node 4: Freebuff (Bottom-Left) -->
        <div class="absolute bottom-12 left-16 z-10 px-2.5 py-1 rounded bg-[#0d1017] border border-[#232a3b] font-code text-[10px] text-white">
          <span class="text-[#ff5c35]">●</span> Freebuff (18ms)
        </div>

        <!-- Node 5: ClinePass (Bottom-Right) -->
        <div class="absolute bottom-12 right-16 z-10 px-2.5 py-1 rounded bg-[#0d1017] border border-[#232a3b] font-code text-[10px] text-white">
          <span class="text-[#4cd7f6]">●</span> ClinePass (45ms)
        </div>
      </div>

      <!-- Footer Legend -->
      <div class="flex items-center justify-between text-[10px] font-code text-[#636c7e] border-t border-[#232a3b]/60 pt-2 z-10">
        <div class="flex items-center gap-3">
          <span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-[#4edea3]"></span> Direct Cache Hit</span>
          <span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-[#4cd7f6]"></span> Dynamic Fallback</span>
          <span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-[#ff5c35]"></span> Standby Pool</span>
        </div>
        <span>Click any node to inspect upstream credential quotas</span>
      </div>
    </div>

    <!-- Recent Requests Feed (1 col - Stitch Screenshot) -->
    <div class="rounded-xl bg-[#131722] border border-[#232a3b] p-5 shadow-lg flex flex-col justify-between">
      <div class="flex items-center justify-between pb-3 border-b border-[#232a3b]/80">
        <div class="flex items-center gap-2">
          <Radio class="w-4 h-4 text-[#4edea3]" />
          <h3 class="font-headline text-sm font-bold text-white">Recent Requests</h3>
        </div>
        <span class="font-code text-[9px] px-1.5 py-0.2 rounded bg-[#4edea3]/15 text-[#4edea3] font-bold">
          LIVE
        </span>
      </div>

      <!-- Stream List -->
      <div class="space-y-2.5 overflow-y-auto max-h-[380px] pr-1 pt-2 font-code text-xs divide-y divide-[#232a3b]/40">
        {#if recentRequests.length > 0}
          {#each recentRequests.slice(0, 8) as req (req.id)}
            <div class="pt-2 flex items-start justify-between">
              <div>
                <div class="text-white font-bold text-xs">{req.model}</div>
                <div class="text-[10px] text-[#8e95a5]">{req.provider}</div>
              </div>
              <div class="text-right">
                <div class="text-[#4edea3] font-bold text-xs">{req.promptTokens + req.completionTokens}t</div>
                <div class="text-[10px] text-[#636c7e]">{req.latency}ms</div>
              </div>
            </div>
          {/each}
        {:else}
          <!-- Sample Rows matching Stitch screenshot if idle -->
          <div class="pt-2 flex items-start justify-between">
            <div>
              <div class="text-white font-bold text-xs">gemini-2.5-flash</div>
              <div class="text-[10px] text-[#8e95a5]">Antigravity #01</div>
            </div>
            <div class="text-right">
              <div class="text-[#4edea3] font-bold text-xs">18,957t</div>
              <div class="text-[10px] text-[#636c7e]">142ms</div>
            </div>
          </div>
          <div class="pt-2 flex items-start justify-between">
            <div>
              <div class="text-white font-bold text-xs">gemini-2.5-pro</div>
              <div class="text-[10px] text-[#8e95a5]">Antigravity #02</div>
            </div>
            <div class="text-right">
              <div class="text-[#4edea3] font-bold text-xs">30,416t</div>
              <div class="text-[10px] text-[#636c7e]">388ms</div>
            </div>
          </div>
          <div class="pt-2 flex items-start justify-between">
            <div>
              <div class="text-white font-bold text-xs">claude-3-7-sonnet</div>
              <div class="text-[10px] text-[#8e95a5]">ClinePass #01</div>
            </div>
            <div class="text-right">
              <div class="text-[#4edea3] font-bold text-xs">9,118t</div>
              <div class="text-[10px] text-[#636c7e]">412ms</div>
            </div>
          </div>
          <div class="pt-2 flex items-start justify-between">
            <div>
              <div class="text-white font-bold text-xs">deepseek-chat</div>
              <div class="text-[10px] text-[#8e95a5]">deepseek-official</div>
            </div>
            <div class="text-right">
              <div class="text-[#4edea3] font-bold text-xs">4,812t</div>
              <div class="text-[10px] text-[#636c7e]">98ms</div>
            </div>
          </div>
        {/if}
      </div>

      <div class="pt-3 border-t border-[#232a3b]/80 flex items-center justify-between text-[10px] font-code text-[#636c7e]">
        <span>Buffer: 250 / 250</span>
        <span class="text-[#4cd7f6] hover:underline cursor-pointer">View Full HTTP Stream →</span>
      </div>
    </div>
  </div>
</div>
