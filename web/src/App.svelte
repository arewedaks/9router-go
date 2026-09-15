<script lang="ts">
  import { Loader2 } from 'lucide-svelte'
  import { api, type APIKey, type Combo, type ProviderConnection, type Settings } from './api/client'
  import AnalyticsView from './components/AnalyticsView.svelte'
  import ApiKeysView from './components/ApiKeysView.svelte'
  import CombosView from './components/CombosView.svelte'
  import ConnectionsView from './components/ConnectionsView.svelte'
  import SettingsView from './components/SettingsView.svelte'
  import Sidebar, { type ActiveTab } from './components/Sidebar.svelte'
  import TerminalView from './components/TerminalView.svelte'
  import TopBar from './components/TopBar.svelte'

  let activeTab = $state<ActiveTab>('connections')
  let connections = $state<ProviderConnection[]>([])
  let combos = $state<Combo[]>([])
  let apiKeys = $state<APIKey[]>([])
  let settings = $state<Settings>({})
  let isLoading = $state(true)
  let isCreateComboOpen = $state(false)

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

  function handleOpenNewCombo() {
    activeTab = 'combos'
    isCreateComboOpen = true
  }
</script>

<div class="flex h-screen w-screen bg-[#0b0e13] text-[#e1e2ea] font-body selection:bg-[#ff5c35]/30 selection:text-white overflow-hidden">
  <!-- Left Sidebar (Fixed 240px from Stitch Design) -->
  <Sidebar
    bind:activeTab
    activeConnections={activeConnectionsCount}
    totalConnections={connections.length}
    onNewCombo={handleOpenNewCombo}
  />

  <!-- Main Viewport (TopBar + Scrollable Canvas) -->
  <div class="flex-1 flex flex-col min-w-0 h-screen overflow-hidden">
    <TopBar onNewCombo={handleOpenNewCombo} />

    <main class="flex-1 overflow-y-auto p-6 bg-[#0b0e13]">
      <div class="max-w-[1560px] mx-auto">
        {#if isLoading}
          <div class="flex flex-col items-center justify-center h-[70vh] gap-3 text-[#8e95a5]">
            <Loader2 class="w-7 h-7 animate-spin text-[#ff5c35]" />
            <span class="font-code text-xs">Connecting to 9Router Localhost Gateway (:20130)...</span>
          </div>
        {:else}
          {#if activeTab === 'connections'}
            <ConnectionsView {connections} onRefresh={loadData} />
          {:else if activeTab === 'combos'}
            <CombosView {combos} onRefresh={loadData} bind:isCreatingOpen={isCreateComboOpen} />
          {:else if activeTab === 'analytics'}
            <AnalyticsView />
          {:else if activeTab === 'terminal'}
            <TerminalView />
          {:else if activeTab === 'keys'}
            <ApiKeysView {apiKeys} onRefresh={loadData} />
          {:else if activeTab === 'settings'}
            <SettingsView {settings} onRefresh={loadData} />
          {/if}
        {/if}
      </div>
    </main>
  </div>
</div>
