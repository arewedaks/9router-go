<script lang="ts">
  import { Check, Copy, Key, Loader2, Plus, Power, Trash2 } from 'lucide-svelte'
  import { api, type APIKey } from '../api/client'

  let {
    apiKeys = [],
    onRefresh
  }: {
    apiKeys: APIKey[]
    onRefresh: () => void
  } = $props()

  let isCreateOpen = $state(false)
  let name = $state('')
  let copiedKey = $state<string | null>(null)
  let isCreating = $state(false)

  function handleCopy(text: string, id: string) {
    navigator.clipboard.writeText(text)
    copiedKey = id
    setTimeout(() => (copiedKey = null), 2000)
  }

  async function handleToggle(key: APIKey) {
    try {
      await api.toggleApiKey(key.id)
      onRefresh()
    } catch (err) {
      alert(`Failed to toggle key: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleDelete(id: string) {
    if (!confirm('Are you sure you want to revoke this API key?')) return
    try {
      await api.deleteApiKey(id)
      onRefresh()
    } catch (err) {
      alert(`Failed to delete key: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleCreate(e: SubmitEvent) {
    e.preventDefault()
    try {
      isCreating = true
      await api.createApiKey({ name: name || 'client-key' })
      isCreateOpen = false
      name = ''
      onRefresh()
    } catch (err) {
      alert(`Failed to create key: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isCreating = false
    }
  }

  let primaryKey = $derived(apiKeys[0]?.key || 'sk-your-token-here')
</script>

<div class="p-6 max-w-7xl mx-auto space-y-6">
  <!-- Header -->
  <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
    <div>
      <h2 class="text-xl font-bold text-white tracking-tight flex items-center gap-2">
        <span>Client API Keys</span>
        <span class="text-xs font-semibold px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 border border-slate-700">
          {apiKeys.length} keys
        </span>
      </h2>
      <p class="text-xs text-slate-400">
        Authorization Bearer tokens for connecting clients (Cursor, Claude Code, omp, Cline)
      </p>
    </div>

    <button
      type="button"
      onclick={() => (isCreateOpen = true)}
      class="flex items-center gap-2 px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold transition shadow-lg shadow-indigo-600/20 cursor-pointer"
    >
      <Plus class="w-4 h-4" />
      <span>Generate API Key</span>
    </button>
  </div>

  <!-- Keys List -->
  <div class="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
    <div class="overflow-x-auto">
      <table class="w-full text-left text-xs">
        <thead>
          <tr class="border-b border-slate-800 text-slate-400 font-medium">
            <th class="py-3 px-4">Name</th>
            <th class="py-3 px-4">Key</th>
            <th class="py-3 px-4">Status</th>
            <th class="py-3 px-4">Created</th>
            <th class="py-3 px-4 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-slate-800/50 font-mono">
          {#each apiKeys as k (k.id)}
            {@const isActive = k.isActive === 1}
            <tr class="hover:bg-slate-800/20 transition">
              <td class="py-3 px-4 font-sans font-bold text-white">{k.name || 'Unnamed Key'}</td>
              <td class="py-3 px-4 text-slate-300">
                <div class="flex items-center gap-2">
                  <span class="bg-slate-950 px-2.5 py-1 rounded-lg border border-slate-800 text-[11px]">
                    {k.key}
                  </span>
                  <button
                    type="button"
                    onclick={() => handleCopy(k.key, k.id)}
                    class="p-1 rounded text-slate-400 hover:text-white cursor-pointer"
                    title="Copy Key"
                  >
                    {#if copiedKey === k.id}
                      <Check class="w-3.5 h-3.5 text-emerald-400" />
                    {:else}
                      <Copy class="w-3.5 h-3.5" />
                    {/if}
                  </button>
                </div>
              </td>
              <td class="py-3 px-4">
                <span
                  class="px-2 py-0.5 rounded text-[10px] font-bold {isActive
                    ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                    : 'bg-slate-800 text-slate-500'}"
                >
                  {isActive ? 'ACTIVE' : 'REVOKED'}
                </span>
              </td>
              <td class="py-3 px-4 text-slate-400 font-sans">
                {k.createdAt ? new Date(k.createdAt).toLocaleDateString() : '—'}
              </td>
              <td class="py-3 px-4 text-right">
                <div class="flex items-center justify-end gap-1">
                  <button
                    type="button"
                    onclick={() => handleToggle(k)}
                    class="p-1.5 rounded-lg border transition cursor-pointer {isActive
                      ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400'
                      : 'bg-slate-800 border-slate-700 text-slate-500'}"
                    title={isActive ? 'Deactivate' : 'Activate'}
                  >
                    <Power class="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    onclick={() => handleDelete(k.id)}
                    class="p-1.5 rounded-lg text-slate-500 hover:text-rose-400 hover:bg-rose-500/10 transition cursor-pointer"
                    title="Delete"
                  >
                    <Trash2 class="w-3.5 h-3.5" />
                  </button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>

  <!-- Ready-to-use Configuration Snippets -->
  <div class="space-y-3 pt-4">
    <h3 class="text-sm font-bold text-white flex items-center gap-2">
      <Key class="w-4 h-4 text-indigo-400" />
      <span>Quick Client Configuration Snippets</span>
    </h3>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <!-- Cursor -->
      <div class="p-4 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
        <div class="flex items-center justify-between">
          <span class="text-xs font-bold text-white">Cursor IDE</span>
          <span class="text-[10px] text-slate-400">Settings &gt; Models &gt; OpenAI API Key</span>
        </div>
        <div class="p-3 rounded-xl bg-slate-950 border border-slate-800/80 font-mono text-[11px] text-slate-300 space-y-1">
          <div>
            <span class="text-slate-500">Base URL: </span>
            <span class="text-indigo-300">http://localhost:20130/v1</span>
          </div>
          <div>
            <span class="text-slate-500">API Key: </span>
            <span class="text-emerald-300 truncate">{primaryKey}</span>
          </div>
        </div>
      </div>

      <!-- Claude Code -->
      <div class="p-4 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
        <div class="flex items-center justify-between">
          <span class="text-xs font-bold text-white">Claude Code CLI</span>
          <span class="text-[10px] text-slate-400">Terminal Environment</span>
        </div>
        <div class="p-3 rounded-xl bg-slate-950 border border-slate-800/80 font-mono text-[11px] text-slate-300 space-y-1">
          <div>export ANTHROPIC_BASE_URL="http://localhost:20130"</div>
          <div>export ANTHROPIC_API_KEY="{primaryKey}"</div>
        </div>
      </div>
    </div>
  </div>

  <!-- Create Modal -->
  {#if isCreateOpen}
    <div class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-center justify-center p-4">
      <div class="bg-slate-900 border border-slate-800 rounded-3xl w-full max-w-md p-6 shadow-2xl space-y-4">
        <h3 class="text-base font-bold text-white flex items-center gap-2">
          <Plus class="w-4 h-4 text-indigo-400" />
          <span>Create New Client Key</span>
        </h3>

        <form onsubmit={handleCreate} class="space-y-4">
          <div>
            <label for="key-label-input" class="block text-xs font-semibold text-slate-300 mb-1">Key Label</label>
            <input
              id="key-label-input"
              type="text"
              placeholder="e.g. cursor-laptop, omp-workstation"
              bind:value={name}
              class="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
            />
          </div>

          <div class="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onclick={() => (isCreateOpen = false)}
              class="px-4 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-300 text-xs font-bold cursor-pointer"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isCreating}
              class="px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold flex items-center gap-1.5 cursor-pointer"
            >
              {#if isCreating}
                <Loader2 class="w-3.5 h-3.5 animate-spin" />
              {:else}
                <Check class="w-3.5 h-3.5" />
              {/if}
              <span>Generate Key</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  {/if}
</div>
