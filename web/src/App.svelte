<script lang="ts">
  import { Loader2 } from 'lucide-svelte'
  import { api, type APIKey, type Combo, type ProviderConnection, type Settings } from './api/client'
  import AnalyticsView from './components/AnalyticsView.svelte'
  import ApiKeysView from './components/ApiKeysView.svelte'
  import CombosView from './components/CombosView.svelte'
  import ConnectionsView from './components/ConnectionsView.svelte'
  import Header, { type ActiveTab } from './components/Header.svelte'
  import SettingsView from './components/SettingsView.svelte'

  let activeTab = $state<ActiveTab>('connections')
  let connections = $state<ProviderConnection[]>([])
  let combos = $state<Combo[]>([])
  let apiKeys = $state<APIKey[]>([])
  let settings = $state<Settings>({})
  let isLoading = $state(true)

  async function loadData() {
    try {
      const [connsRes, combosRes, keysRes, settingsRes] = await Promise.all([
        api.getConnections().catch(() => []),
        api.getCombos().catch(() => []),
        api.getApiKeys().catch(() => []),
        api.getSettings().catch(() => ({})),
      ])
      connections = connsRes
      combos = combosRes
      apiKeys = keysRes
      settings = settingsRes
    } finally {
      isLoading = false
    }
  }

  $effect(() => {
    loadData()
  })

  let activeConnectionsCount = $derived(connections.filter((c) => c.isActive === 1).length)
</script>

<div class="min-h-screen bg-[#090d16] text-slate-100 flex flex-col font-sans">
  <Header
    bind:activeTab
    totalConnections={connections.length}
    activeConnections={activeConnectionsCount}
  />

  <main class="flex-1">
    {#if isLoading}
      <div class="flex flex-col items-center justify-center h-[70vh] gap-3 text-slate-400">
        <Loader2 class="w-6 h-6 animate-spin text-indigo-500" />
        <span class="text-xs font-medium">Loading 9router native Svelte dashboard...</span>
      </div>
    {:else}
      {#if activeTab === 'connections'}
        <ConnectionsView {connections} onRefresh={loadData} />
      {:else if activeTab === 'combos'}
        <CombosView {combos} onRefresh={loadData} />
      {:else if activeTab === 'analytics'}
        <AnalyticsView />
      {:else if activeTab === 'keys'}
        <ApiKeysView {apiKeys} onRefresh={loadData} />
      {:else if activeTab === 'settings'}
        <SettingsView {settings} onRefresh={loadData} />
      {/if}
    {/if}
  </main>
</div>
