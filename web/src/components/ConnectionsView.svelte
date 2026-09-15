<script lang="ts">
  import {
    Activity,
    Check,
    Globe,
    Key,
    Layers,
    Loader2,
    Plus,
    Power,
    RefreshCw,
    Search,
    Shield,
    Trash2,
    Zap
  } from 'lucide-svelte'
  import { api, type ProviderConnection } from '../api/client'

  let {
    connections = [],
    onRefresh
  }: {
    connections: ProviderConnection[]
    onRefresh: () => void
  } = $props()

  let search = $state('')
  let filterType = $state<'all' | 'oauth' | 'apikey' | 'active'>('all')

  // Modals
  let isAddOpen = $state(false)
  let isFreebuffModalOpen = $state(false)
  let isAntigravityModalOpen = $state(false)

  // Add Form
  let formProvider = $state('openai')
  let formName = $state('')
  let formApiKey = $state('')
  let formBaseUrl = $state('')
  let formPriority = $state(1)
  let isSubmitting = $state(false)
  let isPingingAll = $state(false)
  let pingStatus = $state<string | null>(null)

  // Latency cache per connection ID
  let latencyMap = $state<Record<string, number | 'error' | 'testing'>>({})

  // Freebuff Device Flow State
  let fbAuthCode = $state('')
  let fbFingerprint = $state('')
  let fbStatus = $state<'idle' | 'polling' | 'success' | 'error'>('idle')
  let fbMessage = $state('')

  let filteredConnections = $derived(
    connections.filter((c) => {
      const matchSearch =
        c.name.toLowerCase().includes(search.toLowerCase()) ||
        c.provider.toLowerCase().includes(search.toLowerCase()) ||
        c.id.toLowerCase().includes(search.toLowerCase())

      if (!matchSearch) return false
      if (filterType === 'active') return c.isActive === 1
      if (filterType === 'oauth') return c.type === 'oauth'
      if (filterType === 'apikey') return c.type === 'apikey'
      return true
    })
  )

  let activeCount = $derived(connections.filter((c) => c.isActive === 1).length)
  let oauthCount = $derived(connections.filter((c) => c.type === 'oauth').length)
  let apikeyCount = $derived(connections.filter((c) => c.type === 'apikey').length)

  // Split into OAuth Pools vs Custom API Key Proxies
  let oauthConnections = $derived(filteredConnections.filter((c) => c.type === 'oauth'))
  let apikeyConnections = $derived(filteredConnections.filter((c) => c.type !== 'oauth'))

  async function handleToggle(conn: ProviderConnection) {
    try {
      await api.toggleConnection(conn.id)
      onRefresh()
    } catch (err) {
      alert(`Failed to toggle: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleDelete(conn: ProviderConnection) {
    if (!confirm(`Revoke and delete connection "${conn.name || conn.id}"?`)) return
    try {
      await api.deleteConnection(conn.id)
      onRefresh()
    } catch (err) {
      alert(`Failed to delete: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handlePriorityChange(conn: ProviderConnection, newPriority: number) {
    try {
      await api.updateConnectionPriority(conn.id, newPriority)
      onRefresh()
    } catch (err) {
      alert(`Failed to update priority: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleTestConnection(conn: ProviderConnection) {
    latencyMap[conn.id] = 'testing'
    const start = performance.now()
    try {
      const res = await fetch('/v1/models', {
        headers: { 'x-provider': conn.provider },
      })
      const rtt = Math.round(performance.now() - start)
      if (res.ok) {
        latencyMap[conn.id] = rtt
      } else {
        latencyMap[conn.id] = 'error'
      }
    } catch {
      latencyMap[conn.id] = 'error'
    }
  }

  async function handleTestAll() {
    isPingingAll = true
    pingStatus = 'Pinging all active connections...'
    for (const conn of connections.filter((c) => c.isActive === 1)) {
      handleTestConnection(conn)
    }
    setTimeout(() => {
      isPingingAll = false
      pingStatus = 'All endpoints checked'
      setTimeout(() => (pingStatus = null), 3000)
    }, 1500)
  }

  async function handleAddSubmit(e: SubmitEvent) {
    e.preventDefault()
    if (!formProvider || !formApiKey) return
    isSubmitting = true
    try {
      await api.createConnection({
        provider: formProvider,
        name: formName || `${formProvider}-custom`,
        apiKey: formApiKey,
        baseUrl: formBaseUrl || undefined,
        priority: formPriority,
      })
      isAddOpen = false
      formName = ''
      formApiKey = ''
      formBaseUrl = ''
      onRefresh()
    } catch (err) {
      alert(`Failed to create connection: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSubmitting = false
    }
  }

  async function startFreebuffFlow() {
    const chars = '0123456789abcdef'
    let code = ''
    for (let i = 0; i < 32; i++) code += chars[Math.floor(Math.random() * chars.length)]
    fbAuthCode = code
    fbFingerprint = 'cli_' + code.substring(0, 16)
    fbStatus = 'polling'
    fbMessage = 'Browser window opened. Log in on Freebuff to authorize token...'

    window.open(`https://freebuff.com/login?auth_code=${fbAuthCode}`, '_blank')

    const pollInterval = setInterval(async () => {
      if (fbStatus !== 'polling') {
        clearInterval(pollInterval)
        return
      }
      try {
        const res = await api.pollFreebuffToken(fbAuthCode, fbFingerprint)
        if (res && res.status === 'success') {
          clearInterval(pollInterval)
          fbStatus = 'success'
          fbMessage = 'Freebuff authorized successfully! Session credentials stored.'
          onRefresh()
        }
      } catch {
        // continue polling
      }
    }, 2500)

    setTimeout(() => {
      if (fbStatus === 'polling') {
        clearInterval(pollInterval)
        fbStatus = 'error'
        fbMessage = 'Device authorization timed out after 3 minutes. Please try again.'
      }
    }, 180000)
  }
</script>

<div class="p-4 sm:p-6 lg:p-8 max-w-[1560px] mx-auto space-y-6">
  <!-- Top Banner & Action Cluster -->
  <div class="flex flex-col md:flex-row md:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[11px] uppercase tracking-wider text-primary-container px-2 py-0.5 rounded bg-primary-container/10 border border-primary-container/20 font-bold">
          Upstream Matrix
        </span>
        <span class="font-code text-[11px] text-tertiary flex items-center gap-1.5 font-medium">
          <span class="w-1.5 h-1.5 rounded-full bg-tertiary animate-pulse"></span>
          {activeCount} Active Nodes
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-on-surface tracking-tight">
        Providers & Connection Hub
      </h1>
      <p class="font-body text-xs sm:text-sm text-on-surface-variant max-w-2xl leading-relaxed">
        Manage upstream LLM accounts, high-availability multi-account OAuth pools, and custom API proxies with sub-millisecond automated failover.
      </p>
    </div>

    <!-- Action Cluster -->
    <div class="flex flex-wrap items-center gap-2">
      <button
        type="button"
        onclick={() => (isAddOpen = true)}
        class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-primary-container hover:brightness-110 text-on-primary font-body text-xs font-bold shadow-md shadow-primary-container/25 transition cursor-pointer"
      >
        <Plus class="w-4 h-4" />
        <span>Add API Provider</span>
      </button>

      <button
        type="button"
        onclick={() => {
          isFreebuffModalOpen = true
          startFreebuffFlow()
        }}
        class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-surface-container-high hover:bg-surface-container-highest text-on-surface font-body text-xs font-semibold border border-surface-container-highest transition cursor-pointer"
      >
        <Zap class="w-4 h-4 text-tertiary" />
        <span>Connect Freebuff</span>
      </button>

      <button
        type="button"
        onclick={() => (isAntigravityModalOpen = true)}
        class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-surface-container-high hover:bg-surface-container-highest text-on-surface font-body text-xs font-semibold border border-surface-container-highest transition cursor-pointer"
      >
        <Shield class="w-4 h-4 text-secondary" />
        <span>Connect Google AI</span>
      </button>

      <button
        type="button"
        onclick={handleTestAll}
        disabled={isPingingAll}
        class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-surface-container-low hover:bg-surface-container text-on-surface-variant hover:text-on-surface font-body text-xs font-medium border border-surface-container-high transition cursor-pointer"
      >
        {#if isPingingAll}
          <Loader2 class="w-4 h-4 animate-spin text-secondary" />
        {:else}
          <Activity class="w-4 h-4 text-secondary" />
        {/if}
        <span>{pingStatus || 'Test All Handshakes'}</span>
      </button>
    </div>
  </div>

  <!-- Search & Filter Rail -->
  <div class="flex flex-col sm:flex-row items-center justify-between gap-3 p-2 rounded-xl bg-surface-container-low border border-surface-container-high">
    <div class="flex items-center gap-1 overflow-x-auto w-full sm:w-auto pb-1 sm:pb-0">
      <button
        type="button"
        onclick={() => (filterType = 'all')}
        class="px-3 py-1.5 rounded-lg text-xs font-semibold flex items-center gap-1.5 transition cursor-pointer {filterType === 'all'
          ? 'bg-surface-container-highest text-on-surface shadow-sm'
          : 'text-on-surface-variant hover:text-on-surface hover:bg-surface-container'}"
      >
        <span>All Nodes</span>
        <span class="font-code text-[10px] px-1.5 py-0.2 rounded bg-surface-container text-secondary">
          {connections.length}
        </span>
      </button>

      <button
        type="button"
        onclick={() => (filterType = 'active')}
        class="px-3 py-1.5 rounded-lg text-xs font-semibold flex items-center gap-1.5 transition cursor-pointer {filterType === 'active'
          ? 'bg-surface-container-highest text-on-surface shadow-sm'
          : 'text-on-surface-variant hover:text-on-surface hover:bg-surface-container'}"
      >
        <span>Active</span>
        <span class="font-code text-[10px] px-1.5 py-0.2 rounded bg-tertiary/10 text-tertiary">
          {activeCount}
        </span>
      </button>

      <button
        type="button"
        onclick={() => (filterType = 'oauth')}
        class="px-3 py-1.5 rounded-lg text-xs font-semibold flex items-center gap-1.5 transition cursor-pointer {filterType === 'oauth'
          ? 'bg-surface-container-highest text-on-surface shadow-sm'
          : 'text-on-surface-variant hover:text-on-surface hover:bg-surface-container'}"
      >
        <span>OAuth Pools</span>
        <span class="font-code text-[10px] px-1.5 py-0.2 rounded bg-surface-container text-on-surface-variant">
          {oauthCount}
        </span>
      </button>

      <button
        type="button"
        onclick={() => (filterType = 'apikey')}
        class="px-3 py-1.5 rounded-lg text-xs font-semibold flex items-center gap-1.5 transition cursor-pointer {filterType === 'apikey'
          ? 'bg-surface-container-highest text-on-surface shadow-sm'
          : 'text-on-surface-variant hover:text-on-surface hover:bg-surface-container'}"
      >
        <span>API Keys</span>
        <span class="font-code text-[10px] px-1.5 py-0.2 rounded bg-surface-container text-on-surface-variant">
          {apikeyCount}
        </span>
      </button>
    </div>

    <!-- Search Input with ⌘F badge -->
    <div class="relative w-full sm:w-80 flex items-center">
      <Search class="absolute left-3 w-4 h-4 text-outline pointer-events-none" />
      <input
        type="text"
        bind:value={search}
        placeholder="Filter by name, provider, or ID..."
        class="w-full bg-surface-container border border-surface-container-high rounded-lg pl-9 pr-12 py-1.5 font-body text-xs text-on-surface placeholder:text-outline focus:outline-none focus:border-primary-container transition"
      />
      <span class="absolute right-2 px-1.5 py-0.5 rounded bg-surface-container-highest font-code text-[10px] text-outline pointer-events-none">
        ⌘F
      </span>
    </div>
  </div>

  <!-- SECTION 1: OAuth High-Availability Pools -->
  {#if oauthConnections.length > 0}
    <div class="space-y-3">
      <div class="flex items-center justify-between px-1">
        <div class="flex items-center gap-2">
          <Shield class="w-4 h-4 text-secondary" />
          <h2 class="font-headline text-base font-bold text-on-surface">OAuth High-Availability Pools</h2>
        </div>
        <span class="font-code text-[11px] text-outline">Virtual Session Balancing · Auto-Rotating</span>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
        {#each oauthConnections as conn (conn.id)}
          {@const isActive = conn.isActive === 1}
          {@const latency = latencyMap[conn.id]}

          <div
            class="rounded-xl bg-surface-container/70 border border-surface-container-high hover:border-primary-container/40 p-4 transition-all flex flex-col justify-between gap-3 shadow-md"
          >
            <!-- Card Header -->
            <div class="flex items-start justify-between gap-2">
              <div class="flex items-center gap-3">
                <div class="w-10 h-10 rounded-xl bg-surface-container-lowest border border-surface-container-high flex items-center justify-center font-bold text-xs font-code text-secondary">
                  {conn.provider.substring(0, 2).toUpperCase()}
                </div>
                <div>
                  <div class="flex items-center gap-2">
                    <span class="font-headline text-xs font-bold text-on-surface capitalize">
                      {conn.provider}
                    </span>
                    <span class="font-code text-[10px] px-1.5 py-0.2 rounded bg-surface-container-highest text-secondary border border-surface-container-high">
                      OAuth
                    </span>
                  </div>
                  <div class="font-body text-[11px] text-on-surface-variant truncate max-w-[180px]">
                    {conn.name || 'Personal Account'}
                  </div>
                </div>
              </div>

              <!-- Power Toggle -->
              <button
                type="button"
                onclick={() => handleToggle(conn)}
                class="p-1.5 rounded-lg border transition cursor-pointer {isActive
                  ? 'bg-tertiary/10 border-tertiary/30 text-tertiary'
                  : 'bg-surface-container-highest border-surface-container-high text-outline'}"
                title={isActive ? 'Disable account' : 'Enable account'}
              >
                <Power class="w-3.5 h-3.5" />
              </button>
            </div>

            <!-- Meta / Telemetry -->
            <div class="flex items-center justify-between text-[11px] font-code pt-1 border-t border-surface-container-high/60">
              <div class="flex items-center gap-2">
                <span class="text-outline">RTT:</span>
                {#if latency === 'testing'}
                  <Loader2 class="w-3 h-3 animate-spin text-secondary" />
                {:else if latency === 'error'}
                  <span class="text-error font-semibold">Error</span>
                {:else if typeof latency === 'number'}
                  <span class="text-tertiary font-semibold">{latency}ms</span>
                {:else}
                  <button
                    type="button"
                    onclick={() => handleTestConnection(conn)}
                    class="text-secondary hover:underline cursor-pointer"
                  >
                    Ping
                  </button>
                {/if}
              </div>

              <div class="flex items-center gap-2">
                <span class="text-outline">Priority:</span>
                <select
                  value={conn.priority}
                  onchange={(e) => handlePriorityChange(conn, Number(e.currentTarget.value))}
                  class="bg-surface-container-lowest border border-surface-container-high text-on-surface text-[10px] font-code rounded px-1.5 py-0.5 cursor-pointer focus:outline-none"
                >
                  <option value={1}>P1</option>
                  <option value={2}>P2</option>
                  <option value={3}>P3</option>
                  <option value={4}>P4</option>
                </select>
                <button
                  type="button"
                  onclick={() => handleDelete(conn)}
                  class="text-outline hover:text-error transition cursor-pointer p-0.5"
                  title="Delete connection"
                >
                  <Trash2 class="w-3 h-3" />
                </button>
              </div>
            </div>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <!-- SECTION 2: API Key Connections & Custom Proxies -->
  <div class="space-y-3">
    <div class="flex items-center justify-between px-1">
      <div class="flex items-center gap-2">
        <Key class="w-4 h-4 text-primary-container" />
        <h2 class="font-headline text-base font-bold text-on-surface">API Key Connections & Custom Proxies</h2>
      </div>
      <span class="font-code text-[11px] text-outline">{apikeyConnections.length} endpoints configured</span>
    </div>

    {#if apikeyConnections.length > 0}
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
        {#each apikeyConnections as conn (conn.id)}
          {@const isActive = conn.isActive === 1}
          {@const latency = latencyMap[conn.id]}

          <div
            class="rounded-xl bg-surface-container/70 border border-surface-container-high hover:border-primary-container/40 p-4 transition-all flex flex-col justify-between gap-3 shadow-md"
          >
            <!-- Card Header -->
            <div class="flex items-start justify-between gap-2">
              <div class="flex items-center gap-3">
                <div class="w-10 h-10 rounded-xl bg-surface-container-lowest border border-surface-container-high flex items-center justify-center font-bold text-xs font-code text-primary-container">
                  {conn.provider.substring(0, 2).toUpperCase()}
                </div>
                <div>
                  <div class="flex items-center gap-2">
                    <span class="font-headline text-xs font-bold text-on-surface capitalize">
                      {conn.name || conn.provider}
                    </span>
                    <span class="font-code text-[10px] px-1.5 py-0.2 rounded bg-surface-container-highest text-on-surface-variant border border-surface-container-high">
                      {conn.provider}
                    </span>
                  </div>
                  <div class="font-code text-[10px] text-outline truncate max-w-[180px]">
                    {conn.baseUrl || 'Default official endpoint'}
                  </div>
                </div>
              </div>

              <!-- Power Toggle -->
              <button
                type="button"
                onclick={() => handleToggle(conn)}
                class="p-1.5 rounded-lg border transition cursor-pointer {isActive
                  ? 'bg-tertiary/10 border-tertiary/30 text-tertiary'
                  : 'bg-surface-container-highest border-surface-container-high text-outline'}"
                title={isActive ? 'Disable account' : 'Enable account'}
              >
                <Power class="w-3.5 h-3.5" />
              </button>
            </div>

            <!-- Meta / Telemetry -->
            <div class="flex items-center justify-between text-[11px] font-code pt-1 border-t border-surface-container-high/60">
              <div class="flex items-center gap-2">
                <span class="text-outline">RTT:</span>
                {#if latency === 'testing'}
                  <Loader2 class="w-3 h-3 animate-spin text-secondary" />
                {:else if latency === 'error'}
                  <span class="text-error font-semibold">Error</span>
                {:else if typeof latency === 'number'}
                  <span class="text-tertiary font-semibold">{latency}ms</span>
                {:else}
                  <button
                    type="button"
                    onclick={() => handleTestConnection(conn)}
                    class="text-secondary hover:underline cursor-pointer"
                  >
                    Ping
                  </button>
                {/if}
              </div>

              <div class="flex items-center gap-2">
                <span class="text-outline">Priority:</span>
                <select
                  value={conn.priority}
                  onchange={(e) => handlePriorityChange(conn, Number(e.currentTarget.value))}
                  class="bg-surface-container-lowest border border-surface-container-high text-on-surface text-[10px] font-code rounded px-1.5 py-0.5 cursor-pointer focus:outline-none"
                >
                  <option value={1}>P1</option>
                  <option value={2}>P2</option>
                  <option value={3}>P3</option>
                  <option value={4}>P4</option>
                </select>
                <button
                  type="button"
                  onclick={() => handleDelete(conn)}
                  class="text-outline hover:text-error transition cursor-pointer p-0.5"
                  title="Delete connection"
                >
                  <Trash2 class="w-3 h-3" />
                </button>
              </div>
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <div class="p-8 text-center text-outline text-xs border border-dashed border-surface-container-high rounded-xl">
        No API key connections matching current filters. Click "Add API Provider" above to configure.
      </div>
    {/if}
  </div>

  <!-- MODAL: Add Custom Provider (Mac-Style Window from Stitch) -->
  {#if isAddOpen}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-surface-container-lowest/80 backdrop-blur-md p-4">
      <div class="w-full max-w-lg p-6 rounded-2xl bg-surface-container-high border border-surface-container-highest shadow-2xl flex flex-col gap-4">
        <!-- Mac-style Window Top Controls & Title -->
        <div class="flex items-center justify-between pb-2 border-b border-surface-container">
          <div class="flex items-center gap-2">
            <button
              type="button"
              aria-label="Close dialog"
              onclick={() => (isAddOpen = false)}
              class="w-3 h-3 rounded-full bg-error hover:brightness-110 cursor-pointer"
            ></button>
            <div class="w-3 h-3 rounded-full bg-outline"></div>
            <div class="w-3 h-3 rounded-full bg-tertiary"></div>
            <span class="ml-2 font-headline text-sm font-bold text-on-surface">
              Add Custom API / Proxy Provider
            </span>
          </div>
        </div>

        <form onsubmit={handleAddSubmit} class="space-y-3">
          <div>
            <label for="provider-select" class="block font-body text-xs font-semibold text-on-surface-variant mb-1">
              Provider Archetype *
            </label>
            <select
              id="provider-select"
              bind:value={formProvider}
              class="w-full bg-surface-container border border-surface-container-high rounded-lg px-3 py-2 font-body text-xs text-on-surface focus:outline-none focus:ring-1 focus:ring-primary-container"
            >
              <option value="openai">OpenAI Compatible (/v1/chat/completions)</option>
              <option value="anthropic">Anthropic Compatible (/v1/messages)</option>
              <option value="deepseek">DeepSeek Official</option>
              <option value="groq">Groq High-Speed Cloud</option>
              <option value="openrouter">OpenRouter Multi-Model Proxy</option>
              <option value="ollama">Ollama Local Instance</option>
              <option value="kiro">Amazon Q / Kiro Gateway</option>
            </select>
          </div>

          <div>
            <label for="provider-name" class="block font-body text-xs font-semibold text-on-surface-variant mb-1">
              Display Name
            </label>
            <input
              id="provider-name"
              type="text"
              placeholder="e.g. Anthropic Production Key"
              bind:value={formName}
              class="w-full bg-surface-container border border-surface-container-high rounded-lg px-3 py-2 font-body text-xs text-on-surface focus:outline-none focus:ring-1 focus:ring-primary-container"
            />
          </div>

          <div>
            <label for="base-url-input" class="block font-body text-xs font-semibold text-on-surface-variant mb-1">
              Custom Base URL (Optional)
            </label>
            <input
              id="base-url-input"
              type="text"
              placeholder="https://api.anthropic.com/v1"
              bind:value={formBaseUrl}
              class="w-full bg-surface-container border border-surface-container-high rounded-lg px-3 py-2 font-code text-xs text-on-surface focus:outline-none focus:ring-1 focus:ring-primary-container"
            />
          </div>

          <div>
            <label for="api-key-input" class="block font-body text-xs font-semibold text-on-surface-variant mb-1">
              API Key Secret *
            </label>
            <input
              id="api-key-input"
              type="password"
              placeholder="sk-..."
              bind:value={formApiKey}
              required
              class="w-full bg-surface-container border border-surface-container-high rounded-lg px-3 py-2 font-code text-xs text-on-surface focus:outline-none focus:ring-1 focus:ring-primary-container"
            />
          </div>

          <div>
            <label for="priority-select" class="block font-body text-xs font-semibold text-on-surface-variant mb-1">
              Failover Priority
            </label>
            <select
              id="priority-select"
              bind:value={formPriority}
              class="w-full bg-surface-container border border-surface-container-high rounded-lg px-3 py-2 font-code text-xs text-on-surface focus:outline-none focus:ring-1 focus:ring-primary-container"
            >
              <option value={1}>P1 - Primary</option>
              <option value={2}>P2 - Secondary</option>
              <option value={3}>P3 - Backup</option>
              <option value={4}>P4 - Cold Reserve</option>
            </select>
          </div>

          <div class="flex items-center justify-end gap-2 pt-3 border-t border-surface-container">
            <button
              type="button"
              onclick={() => (isAddOpen = false)}
              class="px-4 py-2 rounded-lg font-body text-xs text-on-surface-variant hover:text-on-surface cursor-pointer"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isSubmitting}
              class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-primary-container hover:brightness-110 text-on-primary font-body text-xs font-bold shadow-md shadow-primary-container/20 cursor-pointer"
            >
              {#if isSubmitting}
                <Loader2 class="w-3.5 h-3.5 animate-spin" />
              {:else}
                <Check class="w-3.5 h-3.5" />
              {/if}
              <span>Save Connection</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  {/if}

  <!-- MODAL: Freebuff Device Flow (Stitch Style) -->
  {#if isFreebuffModalOpen}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-surface-container-lowest/80 backdrop-blur-md p-4">
      <div class="w-full max-w-md p-6 rounded-2xl bg-surface-container-high border border-surface-container-highest shadow-2xl space-y-4">
        <div class="flex items-center justify-between pb-2 border-b border-surface-container">
          <div class="flex items-center gap-2">
            <button
              type="button"
              aria-label="Close dialog"
              onclick={() => (isFreebuffModalOpen = false)}
              class="w-3 h-3 rounded-full bg-error cursor-pointer"
            ></button>
            <div class="w-3 h-3 rounded-full bg-outline"></div>
            <div class="w-3 h-3 rounded-full bg-tertiary"></div>
            <span class="ml-2 font-headline text-sm font-bold text-on-surface">
              Freebuff CLI Device Pairing
            </span>
          </div>
        </div>

        <div class="space-y-3">
          <p class="font-body text-xs text-on-surface-variant leading-relaxed">
            Pair with freebuff.com using device flow. A new browser tab has been launched. Log in and allow authorization.
          </p>

          <div class="p-3.5 rounded-xl bg-surface-container-lowest border border-surface-container-high space-y-1.5 font-code text-xs">
            <div class="text-outline text-[10px] uppercase">Device Auth Code</div>
            <div class="text-secondary font-bold select-all tracking-wider">{fbAuthCode || 'Generating...'}</div>
          </div>

          <div class="flex items-center gap-2 p-3 rounded-xl bg-surface-container text-xs font-body {fbStatus === 'success'
            ? 'text-tertiary bg-tertiary/10 border border-tertiary/20'
            : fbStatus === 'error'
              ? 'text-error bg-error/10 border border-error/20'
              : 'text-on-surface-variant'}">
            {#if fbStatus === 'polling'}
              <Loader2 class="w-4 h-4 animate-spin text-secondary flex-shrink-0" />
            {:else if fbStatus === 'success'}
              <Check class="w-4 h-4 text-tertiary flex-shrink-0" />
            {/if}
            <span class="text-[11px]">{fbMessage}</span>
          </div>

          <div class="flex justify-end pt-2">
            <button
              type="button"
              onclick={() => (isFreebuffModalOpen = false)}
              class="px-4 py-2 rounded-lg bg-surface-container hover:bg-surface-container-highest text-on-surface font-body text-xs font-semibold cursor-pointer"
            >
              Done
            </button>
          </div>
        </div>
      </div>
    </div>
  {/if}

  <!-- MODAL: Antigravity Google OAuth (Stitch Style) -->
  {#if isAntigravityModalOpen}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-surface-container-lowest/80 backdrop-blur-md p-4">
      <div class="w-full max-w-md p-6 rounded-2xl bg-surface-container-high border border-surface-container-highest shadow-2xl space-y-4">
        <div class="flex items-center justify-between pb-2 border-b border-surface-container">
          <div class="flex items-center gap-2">
            <button
              type="button"
              aria-label="Close dialog"
              onclick={() => (isAntigravityModalOpen = false)}
              class="w-3 h-3 rounded-full bg-error cursor-pointer"
            ></button>
            <div class="w-3 h-3 rounded-full bg-outline"></div>
            <div class="w-3 h-3 rounded-full bg-tertiary"></div>
            <span class="ml-2 font-headline text-sm font-bold text-on-surface">
              Google Antigravity OAuth
            </span>
          </div>
        </div>

        <div class="space-y-3 font-body text-xs">
          <p class="text-on-surface-variant leading-relaxed">
            Authenticate a Google AI account to access Gemini 1.5/2.0 Pro and Flash models under Antigravity multi-account failover.
          </p>

          <a
            href="/api/oauth/antigravity/login"
            class="flex items-center justify-center gap-2 w-full py-2.5 rounded-xl bg-primary-container hover:brightness-110 text-on-primary font-bold shadow-md shadow-primary-container/20 transition cursor-pointer"
          >
            <Globe class="w-4 h-4" />
            <span>Launch Google OAuth Login</span>
          </a>

          <div class="flex justify-end pt-2">
            <button
              type="button"
              onclick={() => (isAntigravityModalOpen = false)}
              class="px-4 py-1.5 rounded-lg text-outline hover:text-on-surface cursor-pointer"
            >
              Cancel
            </button>
          </div>
        </div>
      </div>
    </div>
  {/if}
</div>
