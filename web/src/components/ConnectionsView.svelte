<script lang="ts">
  import {
    AlertCircle,
    ArrowDown,
    ArrowUp,
    Check,
    ExternalLink,
    Key,
    Loader2,
    Plus,
    Power,
    RefreshCw,
    Search,
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
  let filterType = $state<'all' | 'oauth' | 'apikey'>('all')
  let isAddModalOpen = $state(false)
  let updatingId = $state<string | null>(null)

  // Add Connection state
  let addMode = $state<'oauth' | 'apikey'>('oauth')
  let oauthProvider = $state<'freebuff' | 'antigravity'>('freebuff')
  let fbFlowState = $state<{
    status: 'idle' | 'initiating' | 'polling' | 'authorized' | 'error'
    loginUrl?: string
    authCode?: string
    error?: string
  }>({ status: 'idle' })

  // API Key Form State
  let apiKeyProvider = $state('deepseek')
  let apiKeyName = $state('')
  let apiKeyValue = $state('')
  let apiBaseUrl = $state('')
  let isSavingKey = $state(false)

  let filteredConnections = $derived(
    connections.filter((c) => {
      const q = search.toLowerCase()
      const matchesSearch =
        c.provider.toLowerCase().includes(q) ||
        (c.name && c.name.toLowerCase().includes(q)) ||
        (c.email && c.email.toLowerCase().includes(q)) ||
        c.id.toLowerCase().includes(q)
      if (!matchesSearch) return false
      if (filterType === 'oauth') return c.authType === 'oauth'
      if (filterType === 'apikey') return c.authType !== 'oauth'
      return true
    })
  )

  async function handleToggleActive(conn: ProviderConnection) {
    try {
      updatingId = conn.id
      const nextActive = conn.isActive === 1 ? 0 : 1
      await api.updateConnection(conn.id, { isActive: nextActive })
      onRefresh()
    } catch (err) {
      alert(`Failed to toggle status: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      updatingId = null
    }
  }

  async function handlePriorityChange(conn: ProviderConnection, delta: number) {
    try {
      updatingId = conn.id
      const currentPriority = conn.priority ?? 999999
      const nextPriority = Math.max(1, currentPriority + delta)
      await api.updateConnection(conn.id, { priority: nextPriority })
      onRefresh()
    } catch (err) {
      alert(`Failed to update priority: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      updatingId = null
    }
  }

  async function handleDelete(id: string) {
    if (!confirm('Are you sure you want to delete this provider connection?')) return
    try {
      updatingId = id
      await api.deleteConnection(id)
      onRefresh()
    } catch (err) {
      alert(`Failed to delete connection: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      updatingId = null
    }
  }

  async function startFreebuffFlow() {
    try {
      fbFlowState = { status: 'initiating' }
      const init = await api.initiateFreebuff()
      fbFlowState = {
        status: 'polling',
        loginUrl: init.loginUrl,
        authCode: init.authCode,
      }

      window.open(init.loginUrl, '_blank')

      const interval = setInterval(async () => {
        try {
          const poll = await api.pollFreebuff(init.fingerprintId, init.fingerprintHash)
          if (poll.status === 'authorized') {
            clearInterval(interval)
            fbFlowState = { status: 'authorized' }
            setTimeout(() => {
              isAddModalOpen = false
              fbFlowState = { status: 'idle' }
              onRefresh()
            }, 1500)
          } else if (poll.status === 'expired') {
            clearInterval(interval)
            fbFlowState = { status: 'error', error: 'Login session expired. Please retry.' }
          }
        } catch {
          // keep polling until timeout
        }
      }, 3000)

      setTimeout(() => clearInterval(interval), 300000)
    } catch (err) {
      fbFlowState = {
        status: 'error',
        error: err instanceof Error ? err.message : String(err),
      }
    }
  }

  async function startAntigravityFlow() {
    try {
      const auth = await api.getAntigravityAuthorizeUrl()
      window.location.href = auth.url || auth.redirectUrl
    } catch (err) {
      alert(`Failed to start Google OAuth: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleSaveApiKey(e: SubmitEvent) {
    e.preventDefault()
    if (!apiKeyValue) {
      alert('API key is required')
      return
    }
    try {
      isSavingKey = true
      const dataObj: Record<string, string> = { apiKey: apiKeyValue }
      if (apiBaseUrl) dataObj.baseUrl = apiBaseUrl

      await api.createConnection({
        provider: apiKeyProvider,
        authType: 'apikey',
        name: apiKeyName || `${apiKeyProvider}-connection`,
        apiKey: apiKeyValue,
        data: JSON.stringify(dataObj),
      })
      isAddModalOpen = false
      apiKeyValue = ''
      apiKeyName = ''
      apiBaseUrl = ''
      onRefresh()
    } catch (err) {
      alert(`Failed to save connection: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSavingKey = false
    }
  }
</script>

<div class="p-6 max-w-7xl mx-auto space-y-6">
  <!-- Top Bar -->
  <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
    <div>
      <h2 class="text-xl font-bold text-white tracking-tight flex items-center gap-2">
        <span>Provider Connections</span>
        <span class="text-xs font-semibold px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 border border-slate-700">
          {connections.length} total
        </span>
      </h2>
      <p class="text-xs text-slate-400">Manage LLM upstream accounts, priority order, and OAuth integrations</p>
    </div>

    <div class="flex items-center gap-2.5 w-full sm:w-auto">
      <button
        type="button"
        onclick={onRefresh}
        class="p-2 rounded-xl bg-slate-800/80 hover:bg-slate-700 text-slate-300 transition border border-slate-700 hover:text-white cursor-pointer"
        title="Refresh list"
      >
        <RefreshCw class="w-4 h-4" />
      </button>
      <button
        type="button"
        onclick={() => (isAddModalOpen = true)}
        class="flex items-center gap-2 px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold transition shadow-lg shadow-indigo-600/20 cursor-pointer"
      >
        <Plus class="w-4 h-4" />
        <span>Add Connection</span>
      </button>
    </div>
  </div>

  <!-- Filters & Search -->
  <div class="flex flex-col sm:flex-row items-center justify-between gap-3 bg-slate-900/60 p-2.5 rounded-2xl border border-slate-800/80">
    <div class="relative w-full sm:w-80">
      <Search class="w-4 h-4 absolute left-3 top-1/2 -translate-y-1/2 text-slate-500" />
      <input
        type="text"
        placeholder="Search provider, email, ID..."
        bind:value={search}
        class="w-full pl-9 pr-4 py-1.5 rounded-xl bg-slate-950/80 border border-slate-800 text-xs text-slate-200 placeholder-slate-500 focus:outline-none focus:border-indigo-500"
      />
    </div>

    <div class="flex items-center gap-1 self-start sm:self-auto">
      {#each (['all', 'oauth', 'apikey'] as const) as type}
        <button
          type="button"
          onclick={() => (filterType = type)}
          class="px-3 py-1 rounded-lg text-xs font-medium capitalize transition cursor-pointer {filterType === type
            ? 'bg-slate-800 text-white font-semibold border border-slate-700'
            : 'text-slate-400 hover:text-slate-200'}"
        >
          {type === 'apikey' ? 'API Key' : type}
        </button>
      {/each}
    </div>
  </div>

  <!-- Connection Grid -->
  <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
    {#each filteredConnections as conn (conn.id)}
      {@const isActive = conn.isActive === 1}
      {@const isBusy = updatingId === conn.id}

      <div
        class="rounded-2xl border p-4 transition-all duration-200 flex flex-col justify-between {isActive
          ? 'bg-slate-900/60 border-slate-800 hover:border-slate-700/80 shadow-sm'
          : 'bg-slate-950/40 border-slate-900 opacity-60'}"
      >
        <div>
          <div class="flex items-start justify-between gap-2 mb-3">
            <div class="flex items-center gap-2.5">
              <div
                class="w-8 h-8 rounded-xl flex items-center justify-center font-bold text-xs {conn.provider.includes('antigravity') || conn.provider.includes('gemini')
                  ? 'bg-blue-500/10 text-blue-400 border border-blue-500/20'
                  : conn.provider.includes('freebuff')
                  ? 'bg-lime-500/10 text-lime-400 border border-lime-500/20'
                  : conn.provider.includes('cline')
                  ? 'bg-amber-500/10 text-amber-400 border border-amber-500/20'
                  : 'bg-indigo-500/10 text-indigo-400 border border-indigo-500/20'}"
              >
                {conn.provider.slice(0, 2).toUpperCase()}
              </div>
              <div>
                <div class="flex items-center gap-1.5">
                  <span class="text-xs font-bold text-white capitalize">{conn.provider}</span>
                  <span
                    class="text-[9px] px-1.5 py-0.2 rounded font-semibold uppercase {conn.authType === 'oauth'
                      ? 'bg-purple-500/10 text-purple-400 border border-purple-500/20'
                      : 'bg-slate-800 text-slate-400'}"
                  >
                    {conn.authType}
                  </span>
                </div>
                <p class="text-[11px] text-slate-400 truncate max-w-[170px]">
                  {conn.name || conn.email || conn.id}
                </p>
              </div>
            </div>

            <button
              type="button"
              onclick={() => handleToggleActive(conn)}
              disabled={isBusy}
              class="p-1.5 rounded-lg border transition cursor-pointer {isActive
                ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400 hover:bg-emerald-500/20'
                : 'bg-slate-800 border-slate-700 text-slate-500 hover:text-slate-300'}"
              title={isActive ? 'Deactivate connection' : 'Activate connection'}
            >
              {#if isBusy}
                <Loader2 class="w-3.5 h-3.5 animate-spin" />
              {:else}
                <Power class="w-3.5 h-3.5" />
              {/if}
            </button>
          </div>

          <div class="space-y-1 text-[11px] text-slate-400 font-mono bg-slate-950/60 p-2 rounded-xl border border-slate-800/60 mb-3">
            <div class="flex justify-between">
              <span class="text-slate-500">Priority:</span>
              <span class="text-slate-300 font-semibold">{conn.priority ?? 'None (999999)'}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-slate-500">ID:</span>
              <span class="text-slate-300 truncate max-w-[130px]" title={conn.id}>
                {conn.id}
              </span>
            </div>
          </div>
        </div>

        <div class="flex items-center justify-between pt-2 border-t border-slate-800/60">
          <div class="flex items-center gap-1">
            <button
              type="button"
              onclick={() => handlePriorityChange(conn, -1)}
              class="p-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white transition cursor-pointer"
              title="Increase Priority (lower number)"
            >
              <ArrowUp class="w-3 h-3" />
            </button>
            <button
              type="button"
              onclick={() => handlePriorityChange(conn, 1)}
              class="p-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white transition cursor-pointer"
              title="Decrease Priority (higher number)"
            >
              <ArrowDown class="w-3 h-3" />
            </button>
          </div>

          <button
            type="button"
            onclick={() => handleDelete(conn.id)}
            class="p-1 rounded text-slate-500 hover:text-rose-400 hover:bg-rose-500/10 transition cursor-pointer"
            title="Delete Connection"
          >
            <Trash2 class="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
    {/each}
  </div>

  <!-- Add Connection Modal -->
  {#if isAddModalOpen}
    <div class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-center justify-center p-4">
      <div class="bg-slate-900 border border-slate-800 rounded-3xl w-full max-w-lg p-6 shadow-2xl relative">
        <div class="flex items-center justify-between mb-5">
          <h3 class="text-base font-bold text-white flex items-center gap-2">
            <Plus class="w-4 h-4 text-indigo-400" />
            <span>Add Provider Connection</span>
          </h3>
          <button
            type="button"
            onclick={() => {
              isAddModalOpen = false
              fbFlowState = { status: 'idle' }
            }}
            class="text-slate-400 hover:text-white text-xs px-2 py-1 rounded-lg bg-slate-800 cursor-pointer"
          >
            ✕ Close
          </button>
        </div>

        <!-- Mode Switcher -->
        <div class="flex rounded-xl bg-slate-950 p-1 border border-slate-800 mb-5">
          <button
            type="button"
            onclick={() => (addMode = 'oauth')}
            class="flex-1 py-1.5 rounded-lg text-xs font-bold transition cursor-pointer {addMode === 'oauth'
              ? 'bg-indigo-600 text-white'
              : 'text-slate-400 hover:text-white'}"
          >
            OAuth Device Flows (Freebuff / Google)
          </button>
          <button
            type="button"
            onclick={() => (addMode = 'apikey')}
            class="flex-1 py-1.5 rounded-lg text-xs font-bold transition cursor-pointer {addMode === 'apikey'
              ? 'bg-indigo-600 text-white'
              : 'text-slate-400 hover:text-white'}"
          >
            API Key Providers
          </button>
        </div>

        {#if addMode === 'oauth'}
          <div class="space-y-4">
            <div class="grid grid-cols-2 gap-3">
              <button
                type="button"
                onclick={() => (oauthProvider = 'freebuff')}
                class="p-3.5 rounded-2xl border text-left transition cursor-pointer {oauthProvider === 'freebuff'
                  ? 'bg-lime-500/10 border-lime-500/30 text-lime-300 shadow-sm'
                  : 'bg-slate-950/60 border-slate-800 text-slate-400 hover:border-slate-700'}"
              >
                <div class="font-bold text-xs mb-1">Freebuff (Codebuff)</div>
                <div class="text-[10px] text-slate-400">Freebucks daily quota (GLM 5.3, Solar Pro, DeepSeek)</div>
              </button>
              <button
                type="button"
                onclick={() => (oauthProvider = 'antigravity')}
                class="p-3.5 rounded-2xl border text-left transition cursor-pointer {oauthProvider === 'antigravity'
                  ? 'bg-blue-500/10 border-blue-500/30 text-blue-300 shadow-sm'
                  : 'bg-slate-950/60 border-slate-800 text-slate-400 hover:border-slate-700'}"
              >
                <div class="font-bold text-xs mb-1">Google Antigravity</div>
                <div class="text-[10px] text-slate-400">Multi-account failover for Gemini 2.5 / 3.7</div>
              </button>
            </div>

            {#if oauthProvider === 'freebuff'}
              <div class="bg-slate-950/80 p-4 rounded-2xl border border-slate-800/80 space-y-3">
                <p class="text-xs text-slate-300">
                  Login using official Freebuff Device Flow. Click the button below to generate a login link.
                </p>

                {#if fbFlowState.status === 'idle'}
                  <button
                    type="button"
                    onclick={startFreebuffFlow}
                    class="w-full py-2.5 rounded-xl bg-lime-600 hover:bg-lime-500 text-slate-950 font-bold text-xs flex items-center justify-center gap-2 transition cursor-pointer"
                  >
                    <Zap class="w-4 h-4" />
                    <span>Start Freebuff Login Flow</span>
                  </button>
                {/if}

                {#if fbFlowState.status === 'initiating'}
                  <div class="flex items-center justify-center py-4 text-xs text-slate-400 gap-2">
                    <Loader2 class="w-4 h-4 animate-spin text-lime-400" />
                    <span>Generating device session...</span>
                  </div>
                {/if}

                {#if fbFlowState.status === 'polling'}
                  <div class="space-y-3">
                    <div class="flex items-center gap-2 text-xs text-lime-400 font-semibold">
                      <Loader2 class="w-4 h-4 animate-spin" />
                      <span>Waiting for browser authorization...</span>
                    </div>
                    <div class="bg-slate-900 p-3 rounded-xl border border-slate-800 text-[11px] space-y-1">
                      <div class="text-slate-400">If browser did not open automatically, visit:</div>
                      <a
                        href={fbFlowState.loginUrl}
                        target="_blank"
                        rel="noreferrer"
                        class="text-indigo-400 underline font-mono break-all flex items-center gap-1"
                      >
                        <span>{fbFlowState.loginUrl}</span>
                        <ExternalLink class="w-3 h-3 flex-shrink-0" />
                      </a>
                    </div>
                  </div>
                {/if}

                {#if fbFlowState.status === 'authorized'}
                  <div class="flex items-center gap-2 text-emerald-400 text-xs font-bold py-2">
                    <Check class="w-4 h-4" />
                    <span>Account authorized and saved successfully!</span>
                  </div>
                {/if}

                {#if fbFlowState.status === 'error'}
                  <div class="flex items-center gap-2 text-rose-400 text-xs font-medium">
                    <AlertCircle class="w-4 h-4" />
                    <span>{fbFlowState.error}</span>
                  </div>
                {/if}
              </div>
            {:else}
              <div class="bg-slate-950/80 p-4 rounded-2xl border border-slate-800/80 space-y-3">
                <p class="text-xs text-slate-300">
                  Connect your Google Account to enable Antigravity Gemini high/low capacity models.
                </p>
                <button
                  type="button"
                  onclick={startAntigravityFlow}
                  class="w-full py-2.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white font-bold text-xs flex items-center justify-center gap-2 transition shadow-lg shadow-blue-600/20 cursor-pointer"
                >
                  <ExternalLink class="w-4 h-4" />
                  <span>Sign in with Google OAuth</span>
                </button>
              </div>
            {/if}
          </div>
        {:else}
          <form onsubmit={handleSaveApiKey} class="space-y-3.5">
            <div>
              <label for="provider-select" class="block text-xs font-semibold text-slate-300 mb-1">Provider</label>
              <select
                id="provider-select"
                bind:value={apiKeyProvider}
                class="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
              >
                <option value="deepseek">DeepSeek (deepseek-chat, coder)</option>
                <option value="groq">Groq (Llama-3, fast inference)</option>
                <option value="openrouter">OpenRouter</option>
                <option value="gemini">Google Gemini (Direct API Key)</option>
                <option value="nvidia">Nvidia NIM</option>
                <option value="openai-compatible-chat">Custom OpenAI-Compatible Endpoint</option>
              </select>
            </div>

            <div>
              <label for="conn-name" class="block text-xs font-semibold text-slate-300 mb-1">Connection Name</label>
              <input
                id="conn-name"
                type="text"
                placeholder="e.g. My Primary DeepSeek"
                bind:value={apiKeyName}
                class="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
              />
            </div>

            <div>
              <label for="conn-key" class="block text-xs font-semibold text-slate-300 mb-1">API Key *</label>
              <input
                id="conn-key"
                type="password"
                placeholder="sk-..."
                bind:value={apiKeyValue}
                required
                class="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
              />
            </div>

            {#if apiKeyProvider === 'openai-compatible-chat'}
              <div>
                <label for="conn-url" class="block text-xs font-semibold text-slate-300 mb-1">Base URL</label>
                <input
                  id="conn-url"
                  type="url"
                  placeholder="https://api.together.xyz/v1"
                  bind:value={apiBaseUrl}
                  class="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                />
              </div>
            {/if}

            <button
              type="submit"
              disabled={isSavingKey}
              class="w-full mt-2 py-2.5 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white font-bold text-xs flex items-center justify-center gap-2 transition shadow-lg shadow-indigo-600/20 cursor-pointer"
            >
              {#if isSavingKey}
                <Loader2 class="w-4 h-4 animate-spin" />
              {:else}
                <Key class="w-4 h-4" />
              {/if}
              <span>Save Connection</span>
            </button>
          </form>
        {/if}
      </div>
    </div>
  {/if}
</div>
