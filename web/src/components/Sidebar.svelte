<script lang="ts">
  import {
    Activity,
    BookOpen,
    Cpu,
    Database,
    Globe,
    Key,
    Layers,
    Radio,
    Settings,
    Shield,
    Terminal,
    Zap
  } from 'lucide-svelte'

  export type ActiveTab = 'analytics' | 'combos' | 'connections' | 'settings' | 'keys' | 'terminal'

  let {
    activeTab = $bindable('connections'),
    activeConnections = 0,
    totalConnections = 0,
    onNewCombo
  }: {
    activeTab: ActiveTab
    activeConnections: number
    totalConnections: number
    onNewCombo?: () => void
  } = $props()
</script>

<aside class="w-60 h-screen bg-[#0d1017] border-r border-[#1e2330] flex flex-col justify-between flex-shrink-0 select-none z-30">
  <!-- Top Section: Brand & Nav -->
  <div class="flex flex-col">
    <!-- Header / Brand -->
    <div class="p-4 border-b border-[#1e2330]/80">
      <div class="flex items-center gap-3">
        <!-- 9Router Geometric Logo -->
        <div class="w-8 h-8 rounded-lg overflow-hidden flex-shrink-0 shadow-md shadow-[#ff5c35]/20">
          <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" fill="none" class="w-full h-full">
            <rect width="48" height="48" rx="10" fill="#181D27"/>
            <rect x="0.5" y="0.5" width="47" height="47" rx="9.5" stroke="#2B3245"/>
            <circle cx="24" cy="24" r="7" fill="#FF5C35"/>
            <circle cx="14" cy="16" r="3.5" fill="#FF8469"/>
            <circle cx="34" cy="16" r="3.5" fill="#38BDF8"/>
            <circle cx="14" cy="32" r="3.5" fill="#34D399"/>
            <circle cx="34" cy="32" r="3.5" fill="#A78BFA"/>
            <path d="M16.5 18L21 21.5M31.5 18L27 21.5M16.5 30L21 26.5M31.5 30L27 26.5" stroke="#94A3B8" stroke-width="1.75" stroke-linecap="round"/>
          </svg>
        </div>

        <div class="flex flex-col">
          <div class="flex items-center gap-1.5">
            <span class="font-headline font-bold text-sm text-[#e1e2ea] tracking-tight">9Router</span>
            <span class="font-code text-[9px] px-1.5 py-0.2 rounded bg-[#03b5d3]/15 text-[#4cd7f6] font-bold">
              v1.8.11
            </span>
          </div>
          <span class="font-body text-[10px] text-[#8e95a5]">AI Gateway Router</span>
        </div>
      </div>

      <!-- Gateway Status Pill -->
      <div class="mt-3 flex items-center justify-between px-2.5 py-1 rounded-md bg-[#131722] border border-[#232a3b] font-code text-[10px]">
        <div class="flex items-center gap-1.5 text-[#8e95a5]">
          <span class="w-1.5 h-1.5 rounded-full bg-[#10b981] animate-pulse"></span>
          <span>Gateway :20130</span>
        </div>
        <span class="text-[#10b981] font-semibold uppercase tracking-wider text-[9px]">ONLINE</span>
      </div>
    </div>

    <!-- Navigation Groups -->
    <div class="p-3 space-y-4 overflow-y-auto">
      <!-- Group 1: Main Architecture -->
      <div class="space-y-1">
        <div class="px-2 font-headline text-[10px] font-bold uppercase tracking-wider text-[#636c7e]">
          Main Architecture
        </div>

        <button
          type="button"
          onclick={() => (activeTab = 'analytics')}
          class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-xs font-body transition-all cursor-pointer {activeTab === 'analytics'
            ? 'bg-[#181d27] text-white font-semibold border-l-2 border-[#ff5c35]'
            : 'text-[#9ca3af] hover:text-white hover:bg-[#131722]'}"
        >
          <Activity class="w-4 h-4 {activeTab === 'analytics' ? 'text-[#ff5c35]' : 'text-[#636c7e]'}" />
          <span>Overview & Usage</span>
        </button>

        <button
          type="button"
          onclick={() => (activeTab = 'combos')}
          class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-xs font-body transition-all cursor-pointer {activeTab === 'combos'
            ? 'bg-[#181d27] text-white font-semibold border-l-2 border-[#ff5c35]'
            : 'text-[#9ca3af] hover:text-white hover:bg-[#131722]'}"
        >
          <Layers class="w-4 h-4 {activeTab === 'combos' ? 'text-[#ff5c35]' : 'text-[#636c7e]'}" />
          <span>Model Combos & Routing</span>
        </button>

        <button
          type="button"
          onclick={() => (activeTab = 'connections')}
          class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-xs font-body transition-all cursor-pointer {activeTab === 'connections'
            ? 'bg-[#181d27] text-white font-semibold border-l-2 border-[#ff5c35]'
            : 'text-[#9ca3af] hover:text-white hover:bg-[#131722]'}"
        >
          <Cpu class="w-4 h-4 {activeTab === 'connections' ? 'text-[#ff5c35]' : 'text-[#636c7e]'}" />
          <span>Providers & Endpoints</span>
        </button>

        <button
          type="button"
          onclick={() => (activeTab = 'settings')}
          class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-xs font-body transition-all cursor-pointer {activeTab === 'settings'
            ? 'bg-[#181d27] text-white font-semibold border-l-2 border-[#ff5c35]'
            : 'text-[#9ca3af] hover:text-white hover:bg-[#131722]'}"
        >
          <Zap class="w-4 h-4 {activeTab === 'settings' ? 'text-[#ff5c35]' : 'text-[#636c7e]'}" />
          <span>Quota & Token Saver</span>
        </button>

        <button
          type="button"
          onclick={() => (activeTab = 'keys')}
          class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-xs font-body transition-all cursor-pointer {activeTab === 'keys'
            ? 'bg-[#181d27] text-white font-semibold border-l-2 border-[#ff5c35]'
            : 'text-[#9ca3af] hover:text-white hover:bg-[#131722]'}"
        >
          <Key class="w-4 h-4 {activeTab === 'keys' ? 'text-[#ff5c35]' : 'text-[#636c7e]'}" />
          <span>CLI & Remote Access</span>
        </button>
      </div>

      <!-- Group 2: Telemetry & Ops -->
      <div class="space-y-1 pt-1">
        <div class="px-2 font-headline text-[10px] font-bold uppercase tracking-wider text-[#636c7e]">
          Telemetry & Ops
        </div>

        <button
          type="button"
          onclick={() => (activeTab = 'terminal')}
          class="w-full flex items-center justify-between px-2.5 py-2 rounded-lg text-xs font-body transition-all cursor-pointer {activeTab === 'terminal'
            ? 'bg-[#181d27] text-white font-semibold border-l-2 border-[#ff5c35]'
            : 'text-[#9ca3af] hover:text-white hover:bg-[#131722]'}"
        >
          <div class="flex items-center gap-2.5">
            <Terminal class="w-4 h-4 {activeTab === 'terminal' ? 'text-[#ff5c35]' : 'text-[#636c7e]'}" />
            <span>Live Console Logs</span>
          </div>
          <span class="font-code text-[9px] px-1.5 py-0.2 rounded bg-[#10b981]/15 text-[#10b981] font-bold">
            LIVE
          </span>
        </button>

        <button
          type="button"
          onclick={() => (activeTab = 'connections')}
          class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-xs font-body text-[#9ca3af] hover:text-white hover:bg-[#131722] transition cursor-pointer"
        >
          <Globe class="w-4 h-4 text-[#636c7e]" />
          <span>Proxy Pools & Health</span>
        </button>

        <button
          type="button"
          onclick={() => (activeTab = 'settings')}
          class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-xs font-body text-[#9ca3af] hover:text-white hover:bg-[#131722] transition cursor-pointer"
        >
          <Settings class="w-4 h-4 text-[#636c7e]" />
          <span>Settings & Security</span>
        </button>
      </div>
    </div>
  </div>

  <!-- Bottom Section: Telemetry Box & Sync Footer -->
  <div class="p-3 border-t border-[#1e2330]/80 space-y-2">
    <!-- Telemetry Readout Box -->
    <div class="p-2.5 rounded-lg bg-[#11141b] border border-[#232a3b] flex items-center justify-between font-code text-[10px]">
      <div>
        <div class="text-[#636c7e]">Latency:</div>
        <div class="text-[#10b981] font-bold">14ms</div>
      </div>
      <div class="text-right">
        <div class="text-[#636c7e]">Velocity:</div>
        <div class="text-[#4cd7f6] font-bold">42.8 r/s</div>
      </div>
    </div>

    <!-- Footer Links -->
    <div class="flex items-center justify-between px-1 text-[10px] font-code text-[#636c7e]">
      <div class="flex items-center gap-1 hover:text-[#9ca3af] cursor-pointer">
        <BookOpen class="w-3 h-3" />
        <span>Docs</span>
      </div>
      <div class="flex items-center gap-1">
        <span>⚡ Sync:00:00</span>
      </div>
    </div>
  </div>
</aside>
