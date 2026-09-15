<script lang="ts">
  import {
    Activity,
    Cpu,
    Key,
    Layers,
    Radio,
    Settings,
    Terminal
  } from 'lucide-svelte'

  export type ActiveTab = 'connections' | 'combos' | 'analytics' | 'terminal' | 'keys' | 'settings'

  let {
    activeTab = $bindable('connections'),
    totalConnections = 0,
    activeConnections = 0,
  }: {
    activeTab: ActiveTab
    totalConnections: number
    activeConnections: number
  } = $props()

  const tabs: { id: ActiveTab; label: string; icon: typeof Cpu }[] = [
    { id: 'connections', label: 'Providers Hub', icon: Cpu },
    { id: 'combos', label: 'Combos & Routing', icon: Layers },
    { id: 'analytics', label: 'Usage & Analytics', icon: Activity },
    { id: 'terminal', label: 'Console Logs', icon: Terminal },
    { id: 'keys', label: 'API Keys', icon: Key },
    { id: 'settings', label: 'Settings', icon: Settings },
  ]
</script>

<header class="border-b border-surface-container-high bg-surface-container-lowest/90 backdrop-blur-xl sticky top-0 z-40">
  <div class="max-w-[1560px] mx-auto px-4 sm:px-6 h-16 flex items-center justify-between gap-4">
    <!-- Brand / Logo -->
    <div class="flex items-center gap-3.5">
      <div class="relative w-9 h-9 rounded-xl overflow-hidden shadow-lg shadow-primary-container/20 flex-shrink-0">
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" fill="none" class="w-full h-full">
          <rect width="48" height="48" rx="12" fill="#181D27"/>
          <rect x="0.5" y="0.5" width="47" height="47" rx="11.5" stroke="#2B3245"/>
          <circle cx="24" cy="24" r="7" fill="#FF5C35"/>
          <circle cx="14" cy="16" r="3.5" fill="#FF8469"/>
          <circle cx="34" cy="16" r="3.5" fill="#38BDF8"/>
          <circle cx="14" cy="32" r="3.5" fill="#34D399"/>
          <circle cx="34" cy="32" r="3.5" fill="#A78BFA"/>
          <path d="M16.5 18L21 21.5M31.5 18L27 21.5M16.5 30L21 26.5M31.5 30L27 26.5" stroke="#94A3B8" stroke-width="1.75" stroke-linecap="round"/>
        </svg>
      </div>

      <div class="flex flex-col">
        <div class="flex items-center gap-2">
          <span class="font-headline text-base font-bold text-on-surface tracking-tight">9Router</span>
          <span class="text-[9px] font-code font-bold uppercase tracking-wider px-1.5 py-0.2 rounded bg-primary-container/15 text-primary-container border border-primary-container/30">
            v1.8.11 · Go + Svelte 5
          </span>
        </div>
        <div class="flex items-center gap-1.5 text-[11px] text-on-surface-variant">
          <span class="w-1.5 h-1.5 rounded-full bg-tertiary animate-pulse"></span>
          <span class="font-code text-[10px] text-tertiary font-medium">
            {activeConnections}/{totalConnections} Nodes Online
          </span>
          <span class="text-outline">•</span>
          <span class="font-code text-[10px] text-secondary">High-Throughput Gateway</span>
        </div>
      </div>
    </div>

    <!-- Navigation Tabs -->
    <nav class="hidden md:flex items-center gap-1 bg-surface-container-low p-1 rounded-xl border border-surface-container-high/60">
      {#each tabs as tab (tab.id)}
        {@const Icon = tab.icon}
        {@const isActive = activeTab === tab.id}
        <button
          type="button"
          onclick={() => (activeTab = tab.id)}
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-all cursor-pointer {isActive
            ? 'bg-primary-container text-on-primary font-bold shadow-sm shadow-primary-container/30'
            : 'text-on-surface-variant hover:text-on-surface hover:bg-surface-container'}"
        >
          <Icon class="w-3.5 h-3.5 {isActive ? 'text-on-primary' : 'text-outline'}" />
          <span>{tab.label}</span>
        </button>
      {/each}
    </nav>

    <!-- Quick Status Pill -->
    <div class="flex items-center gap-2">
      <div class="hidden sm:flex items-center gap-2 px-2.5 py-1 rounded-lg bg-surface-container border border-surface-container-high text-[11px] font-code">
        <span class="text-outline">Port:</span>
        <span class="text-secondary font-semibold">20130</span>
      </div>
      <div class="flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-surface-container border border-surface-container-high text-[11px] font-code text-on-surface">
        <Radio class="w-3 h-3 text-tertiary animate-pulse" />
        <span class="hidden sm:inline text-tertiary font-semibold">Healthy</span>
      </div>
    </div>
  </div>

  <!-- Mobile Navigation Bar -->
  <div class="md:hidden flex items-center justify-around border-t border-surface-container px-2 py-1.5 bg-surface-container-lowest overflow-x-auto">
    {#each tabs as tab (tab.id)}
      {@const Icon = tab.icon}
      {@const isActive = activeTab === tab.id}
      <button
        type="button"
        onclick={() => (activeTab = tab.id)}
        class="flex flex-col items-center gap-0.5 px-2.5 py-1 rounded-lg text-[10px] cursor-pointer {isActive
          ? 'text-primary-container font-bold'
          : 'text-on-surface-variant'}"
      >
        <Icon class="w-4 h-4" />
        <span>{tab.label.split(' ')[0]}</span>
      </button>
    {/each}
  </div>
</header>
