<script lang="ts">
  import {
    Check,
    Copy,
    Key,
    Loader2,
    Plus,
    Power,
    Shield,
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

  let primaryKey = $derived(apiKeys[0]?.key || 'sk-your-token-here')
</script>

<div class="p-4 sm:p-6 lg:p-8 max-w-[1560px] mx-auto space-y-6">
  <!-- Header -->
  <div class="flex flex-col md:flex-row md:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[11px] uppercase tracking-wider text-primary-container px-2 py-0.5 rounded bg-primary-container/10 border border-primary-container/20 font-bold">
          Client Authentication
        </span>
        <span class="text-outline">•</span>
        <span class="font-code text-[11px] text-tertiary">
          {apiKeys.filter((k) => k.isActive === 1).length} Active Keys
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-on-surface tracking-tight">
        Client API Keys & Access Tokens
      </h1>
      <p class="font-body text-xs sm:text-sm text-on-surface-variant max-w-2xl leading-relaxed">
        Issue and manage Bearer tokens for connecting clients (Cursor IDE, Claude Code CLI, omp, Cline) to the local gateway on port 20130.
      </p>
    </div>

    <button
      type="button"
      onclick={() => (isCreateOpen = true)}
      class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-primary-container hover:brightness-110 text-on-primary font-body text-xs font-bold shadow-md shadow-primary-container/25 transition cursor-pointer"
    >
      <Plus class="w-4 h-4" />
      <span>Generate API Key</span>
    </button>
  </div>

  <!-- Keys Table Card (Stitch Theme) -->
  <div class="bg-surface-container-low border border-surface-container-high rounded-2xl p-5 shadow-xl space-y-4">
    <div class="overflow-x-auto">
      <table class="w-full text-left font-body text-xs">
        <thead>
          <tr class="border-b border-surface-container text-outline font-semibold uppercase text-[10px] tracking-wider">
            <th class="py-3 px-4">Label</th>
            <th class="py-3 px-4">Bearer Token</th>
            <th class="py-3 px-4">Status</th>
            <th class="py-3 px-4">Created</th>
            <th class="py-3 px-4 text-right">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-surface-container/60 font-code">
          {#each apiKeys as k (k.id)}
            {@const isActive = k.isActive === 1}
            <tr class="hover:bg-surface-container/30 transition">
              <td class="py-3 px-4 font-body font-bold text-on-surface">{k.name || 'Client Key'}</td>
              <td class="py-3 px-4 text-on-surface-variant">
                <div class="flex items-center gap-2">
                  <span class="bg-surface-container px-2.5 py-1 rounded-md border border-surface-container-high text-[11px] text-secondary">
                    {k.key}
                  </span>
                  <button
                    type="button"
                    onclick={() => handleCopy(k.key, k.id)}
                    class="p-1 rounded text-outline hover:text-on-surface cursor-pointer"
                    title="Copy Key"
                  >
                    {#if copiedKey === k.id}
                      <Check class="w-3.5 h-3.5 text-tertiary" />
                    {:else}
                      <Copy class="w-3.5 h-3.5" />
                    {/if}
                  </button>
                </div>
              </td>
              <td class="py-3 px-4">
                <span
                  class="px-2 py-0.5 rounded text-[10px] font-bold {isActive
                    ? 'bg-tertiary/10 text-tertiary border border-tertiary/20'
                    : 'bg-surface-container text-outline'}"
                >
                  {isActive ? 'ACTIVE' : 'REVOKED'}
                </span>
              </td>
              <td class="py-3 px-4 text-outline font-body text-[11px]">
                {k.createdAt ? new Date(k.createdAt).toLocaleDateString() : '—'}
              </td>
              <td class="py-3 px-4 text-right">
                <div class="flex items-center justify-end gap-1">
                  <button
                    type="button"
                    onclick={() => handleToggle(k)}
                    class="p-1.5 rounded-lg border transition cursor-pointer {isActive
                      ? 'bg-tertiary/10 border-tertiary/20 text-tertiary'
                      : 'bg-surface-container border-surface-container-high text-outline'}"
                    title={isActive ? 'Deactivate' : 'Activate'}
                  >
                    <Power class="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    onclick={() => handleDelete(k.id)}
                    class="p-1.5 rounded-lg text-outline hover:text-error hover:bg-error-container/20 transition cursor-pointer"
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

  <!-- Quick Client Configuration Snippets -->
  <div class="space-y-3 pt-2">
    <h3 class="font-headline text-sm font-bold text-on-surface flex items-center gap-2">
      <Key class="w-4 h-4 text-primary-container" />
      <span>Quick Client Integration Snippets</span>
    </h3>

    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <!-- Cursor -->
      <div class="p-4 rounded-xl bg-surface-container-low border border-surface-container-high space-y-2">
        <div class="flex items-center justify-between">
          <span class="font-headline text-xs font-bold text-on-surface">Cursor IDE</span>
          <span class="font-code text-[10px] text-outline">Settings &gt; Models &gt; OpenAI API Key</span>
        </div>
        <div class="p-3 rounded-lg bg-surface-container border border-surface-container-high font-code text-[11px] text-on-surface space-y-1 select-all">
          <div>
            <span class="text-outline">Base URL: </span>
            <span class="text-secondary">http://localhost:20130/v1</span>
          </div>
          <div>
            <span class="text-outline">API Key: </span>
            <span class="text-primary truncate">{primaryKey}</span>
          </div>
        </div>
      </div>

      <!-- Claude Code -->
      <div class="p-4 rounded-xl bg-surface-container-low border border-surface-container-high space-y-2">
        <div class="flex items-center justify-between">
          <span class="font-headline text-xs font-bold text-on-surface">Claude Code CLI</span>
          <span class="font-code text-[10px] text-outline">Terminal Environment</span>
        </div>
        <div class="p-3 rounded-lg bg-surface-container border border-surface-container-high font-code text-[11px] text-on-surface space-y-1 select-all">
          <div>export ANTHROPIC_BASE_URL="http://localhost:20130"</div>
          <div>export ANTHROPIC_API_KEY="{primaryKey}"</div>
        </div>
      </div>
    </div>
  </div>

  <!-- Create Key Modal (Mac Style) -->
  {#if isCreateOpen}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-surface-container-lowest/80 backdrop-blur-md p-4">
      <div class="w-full max-w-md p-6 rounded-2xl bg-surface-container-high border border-surface-container-highest shadow-2xl space-y-4">
        <div class="flex items-center justify-between pb-2 border-b border-surface-container">
          <div class="flex items-center gap-2">
            <button
              type="button"
              aria-label="Close dialog"
              onclick={() => (isCreateOpen = false)}
              class="w-3 h-3 rounded-full bg-error cursor-pointer"
            ></button>
            <div class="w-3 h-3 rounded-full bg-outline"></div>
            <div class="w-3 h-3 rounded-full bg-tertiary"></div>
            <span class="ml-2 font-headline text-sm font-bold text-on-surface">
              Generate Client API Key
            </span>
          </div>
        </div>

        <form onsubmit={handleCreate} class="space-y-3 font-body text-xs">
          <div>
            <label for="key-name" class="block font-semibold text-on-surface-variant mb-1">Key Label</label>
            <input
              id="key-name"
              type="text"
              placeholder="e.g. cursor-mini-pc, claude-cli-laptop"
              bind:value={name}
              class="w-full bg-surface-container border border-surface-container-high rounded-lg px-3 py-2 font-code text-xs text-on-surface focus:outline-none focus:ring-1 focus:ring-primary-container"
            />
          </div>

          <div class="flex justify-end gap-2 pt-3 border-t border-surface-container">
            <button
              type="button"
              onclick={() => (isCreateOpen = false)}
              class="px-4 py-2 rounded-lg text-on-surface-variant hover:text-on-surface cursor-pointer"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isCreating}
              class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-primary-container hover:brightness-110 text-on-primary font-bold shadow-md shadow-primary-container/20 cursor-pointer"
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
