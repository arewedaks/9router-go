<script lang="ts">
  import {
    Check,
    Copy,
    Key,
    Loader2,
    Plus,
    Power,
    Shield,
    Terminal,
    Trash2
  } from 'lucide-svelte'
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

  let primaryKey = $derived(apiKeys[0]?.key || 'sk-9router-local-token')
</script>

<div class="space-y-6">
  <!-- Header -->
  <div class="flex flex-col sm:flex-row sm:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[10px] uppercase tracking-wider text-[#ff5c35] px-2 py-0.5 rounded bg-[#ff5c35]/10 border border-[#ff5c35]/25 font-bold">
          Client Gateway Access
        </span>
        <span class="text-[#636c7e]">•</span>
        <span class="font-code text-[11px] text-[#4edea3]">
          {apiKeys.filter((k) => k.isActive === 1).length} Active Tokens
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-[#e1e2ea] tracking-tight">
        CLI & Remote Access
      </h1>
      <p class="font-body text-xs sm:text-sm text-[#8e95a5] max-w-2xl leading-relaxed">
        Issue and manage Bearer tokens for connecting clients (Cursor IDE, Claude Code CLI, omp, Cline) to the local gateway on port 20130.
      </p>
    </div>

    <button
      type="button"
      onclick={() => (isCreateOpen = true)}
      class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-[#ff5c35] hover:brightness-110 text-white font-body text-xs font-bold shadow-md shadow-[#ff5c35]/25 transition cursor-pointer"
    >
      <Plus class="w-4 h-4" />
      <span>Generate Client Key</span>
    </button>
  </div>

  <!-- Keys Table Card -->
  <div class="bg-[#131722] border border-[#232a3b] rounded-xl overflow-hidden shadow-xl">
    <div class="p-4 border-b border-[#232a3b] flex items-center justify-between">
      <h3 class="font-headline text-sm font-bold text-white flex items-center gap-2">
        <Key class="w-4 h-4 text-[#ff5c35]" />
        <span>Active Access Tokens</span>
      </h3>
      <span class="font-code text-[11px] text-[#636c7e]">{apiKeys.length} Keys Enrolled</span>
    </div>

    <div class="overflow-x-auto">
      <table class="w-full text-left font-body text-xs">
        <thead>
          <tr class="border-b border-[#232a3b] text-[#636c7e] font-code uppercase text-[10px] tracking-wider bg-[#0d1017]">
            <th class="py-2.5 px-4">Label Identity</th>
            <th class="py-2.5 px-4">Bearer Token</th>
            <th class="py-2.5 px-4">Status</th>
            <th class="py-2.5 px-4">Created Date</th>
            <th class="py-2.5 px-4 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-[#232a3b]/50 font-code">
          {#each apiKeys as k (k.id)}
            {@const isActive = k.isActive === 1}
            <tr class="hover:bg-[#181d27]/40 transition">
              <td class="py-3 px-4 font-body font-bold text-white">{k.name || 'Client Token'}</td>
              <td class="py-3 px-4 text-[#8e95a5]">
                <div class="flex items-center gap-2">
                  <span class="bg-[#0b0e13] px-2.5 py-1 rounded border border-[#232a3b] text-[11px] text-[#4cd7f6]">
                    {k.key}
                  </span>
                  <button
                    type="button"
                    onclick={() => handleCopy(k.key, k.id)}
                    class="p-1 rounded text-[#636c7e] hover:text-white cursor-pointer"
                    title="Copy Key"
                  >
                    {#if copiedKey === k.id}
                      <Check class="w-3.5 h-3.5 text-[#4edea3]" />
                    {:else}
                      <Copy class="w-3.5 h-3.5" />
                    {/if}
                  </button>
                </div>
              </td>
              <td class="py-3 px-4">
                <span
                  class="px-2 py-0.5 rounded text-[10px] font-bold {isActive
                    ? 'bg-[#4edea3]/10 text-[#4edea3] border border-[#4edea3]/20'
                    : 'bg-[#1c2230] text-[#636c7e]'}"
                >
                  {isActive ? 'ACTIVE' : 'REVOKED'}
                </span>
              </td>
              <td class="py-3 px-4 text-[#636c7e] font-body text-[11px]">
                {k.createdAt ? new Date(k.createdAt).toLocaleDateString() : '—'}
              </td>
              <td class="py-3 px-4 text-right">
                <div class="flex items-center justify-end gap-1.5">
                  <button
                    type="button"
                    onclick={() => handleToggle(k)}
                    class="p-1.5 rounded-lg border transition cursor-pointer {isActive
                      ? 'bg-[#4edea3]/10 border-[#4edea3]/20 text-[#4edea3]'
                      : 'bg-[#1c2230] border-[#232a3b] text-[#636c7e]'}"
                    title={isActive ? 'Deactivate' : 'Activate'}
                  >
                    <Power class="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    onclick={() => handleDelete(k.id)}
                    class="p-1.5 rounded-lg text-[#636c7e] hover:text-rose-400 transition cursor-pointer"
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

  <!-- Quick Client Snippets -->
  <div class="space-y-3">
    <h3 class="font-headline text-sm font-bold text-white flex items-center gap-2">
      <Terminal class="w-4 h-4 text-[#4cd7f6]" />
      <span>Quick Client Integration Snippets</span>
    </h3>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <!-- Cursor -->
      <div class="p-4 rounded-xl bg-[#131722] border border-[#232a3b] space-y-2">
        <div class="flex items-center justify-between">
          <span class="font-headline text-xs font-bold text-white">Cursor IDE</span>
          <span class="font-code text-[10px] text-[#636c7e]">Settings &gt; Models &gt; OpenAI API Key</span>
        </div>
        <div class="p-3 rounded-lg bg-[#0b0e13] border border-[#232a3b] font-code text-[11px] text-[#e1e2ea] space-y-1 select-all">
          <div>
            <span class="text-[#636c7e]">Base URL: </span>
            <span class="text-[#4cd7f6]">http://localhost:20130/v1</span>
          </div>
          <div>
            <span class="text-[#636c7e]">API Key: </span>
            <span class="text-[#ff8469] truncate">{primaryKey}</span>
          </div>
        </div>
      </div>

      <!-- Claude Code -->
      <div class="p-4 rounded-xl bg-[#131722] border border-[#232a3b] space-y-2">
        <div class="flex items-center justify-between">
          <span class="font-headline text-xs font-bold text-white">Claude Code CLI</span>
          <span class="font-code text-[10px] text-[#636c7e]">Terminal Environment</span>
        </div>
        <div class="p-3 rounded-lg bg-[#0b0e13] border border-[#232a3b] font-code text-[11px] text-[#e1e2ea] space-y-1 select-all">
          <div>export ANTHROPIC_BASE_URL="http://localhost:20130"</div>
          <div>export ANTHROPIC_API_KEY="{primaryKey}"</div>
        </div>
      </div>
    </div>
  </div>

  <!-- Create Key Modal -->
  {#if isCreateOpen}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-md p-4">
      <div class="w-full max-w-md p-6 rounded-2xl bg-[#181d27] border border-[#2b354a] shadow-2xl space-y-4">
        <div class="flex items-center justify-between pb-2 border-b border-[#232a3b]">
          <div class="flex items-center gap-2">
            <button
              type="button"
              aria-label="Close dialog"
              onclick={() => (isCreateOpen = false)}
              class="w-3 h-3 rounded-full bg-[#ff5f56] cursor-pointer"
            ></button>
            <div class="w-3 h-3 rounded-full bg-[#ffbd2e]"></div>
            <div class="w-3 h-3 rounded-full bg-[#27c93f]"></div>
            <span class="ml-2 font-headline text-sm font-bold text-white">
              Generate Client Access Token
            </span>
          </div>
        </div>

        <form onsubmit={handleCreate} class="space-y-3 font-body text-xs">
          <div>
            <label for="new-key-label" class="block font-semibold text-[#8e95a5] mb-1">Token Label</label>
            <input
              id="new-key-label"
              type="text"
              placeholder="e.g. cursor-mini-pc, claude-cli-laptop"
              bind:value={name}
              class="w-full bg-[#0d1017] border border-[#232a3b] rounded-lg px-3 py-2 font-code text-xs text-white focus:outline-none focus:border-[#ff5c35]"
            />
          </div>

          <div class="flex justify-end gap-2 pt-3 border-t border-[#232a3b]">
            <button
              type="button"
              onclick={() => (isCreateOpen = false)}
              class="px-4 py-2 rounded-lg text-[#8e95a5] hover:text-white cursor-pointer"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isCreating}
              class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-[#ff5c35] hover:brightness-110 text-white font-bold shadow-md shadow-[#ff5c35]/25 cursor-pointer"
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
