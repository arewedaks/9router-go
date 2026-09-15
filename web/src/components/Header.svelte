<script lang="ts">
  import { Activity, Cpu, Key, Layers, Network, Settings as SettingsIcon } from 'lucide-svelte'

  export type ActiveTab = 'connections' | 'combos' | 'analytics' | 'keys' | 'settings'

  let {
    activeTab = $bindable('connections'),
    totalConnections = 0,
    activeConnections = 0,
    version = 'v1.8.11'
  }: {
    activeTab: ActiveTab
    totalConnections: number
    activeConnections: number
    version?: string
  } = $props()

  const tabs = [
    { id: 'connections' as const, label: 'Connections', icon: Network, badge: true },
    { id: 'combos' as const, label: 'Combos & Routing', icon: Layers, badge: false },
    { id: 'analytics' as const, label: 'Usage & Stream', icon: Activity, badge: false },
    { id: 'keys' as const, label: 'API Keys', icon: Key, badge: false },
    { id: 'settings' as const, label: 'Settings', icon: SettingsIcon, badge: false },
  ]
</script>

<header class="border-b border-slate-800 bg-[#0d1322]/90 backdrop-blur sticky top-0 z-40 px-6 py-3.5 flex flex-wrap items-center justify-between gap-4">
  <div class="flex items-center gap-3.5">
    <div class="w-9 h-9 rounded-xl bg-gradient-to-br from-indigo-500 to-emerald-500 flex items-center justify-center shadow-lg shadow-indigo-500/20">
      <Cpu class="w-5 h-5 text-white" />
    </div>
    <div>
      <div class="flex items-center gap-2">
        <h1 class="text-base font-bold tracking-tight text-white">9Router</h1>
        <span class="text-[10px] font-semibold uppercase tracking-wider px-2 py-0.5 rounded-full bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
          Native Go {version} · Svelte 5
        </span>
      </div>
      <p class="text-xs text-slate-400 font-medium">Ultra-low latency AI Gateway & Smart Router</p>
    </div>
  </div>

  <nav class="flex items-center gap-1.5 bg-slate-900/80 p-1 rounded-xl border border-slate-800">
    {#each tabs as tab}
      {@const Icon = tab.icon}
      {@const isActive = activeTab === tab.id}
      <button
        type="button"
        onclick={() => (activeTab = tab.id)}
        class="flex items-center gap-2 px-3.5 py-1.5 rounded-lg text-xs font-semibold transition-all {isActive
          ? 'bg-gradient-to-r from-indigo-600 to-indigo-500 text-white shadow-md shadow-indigo-600/30'
          : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/60'}"
      >
        <Icon class="w-3.5 h-3.5" />
        <span>{tab.label}</span>
        {#if tab.badge}
          <span
            class="text-[10px] px-1.5 py-0.2 rounded-full font-bold {isActive
              ? 'bg-indigo-700 text-white'
              : 'bg-slate-800 text-slate-400'}"
          >
            {activeConnections}/{totalConnections}
          </span>
        {/if}
      </button>
    {/each}
  </nav>
</header>
