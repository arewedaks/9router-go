<script lang="ts">
  import { Check, Loader2, RefreshCw, Save, Shield, Zap } from 'lucide-svelte'
  import { api, type Settings } from '../api/client'

  let {
    settings = {},
    onRefresh
  }: {
    settings: Settings
    onRefresh: () => void
  } = $props()

  let formData = $state<Settings>({})
  let isSaving = $state(false)
  let resetProvider = $state('antigravity')
  let isResetting = $state(false)

  $effect(() => {
    formData = { ...settings }
  })

  async function handleSave() {
    try {
      isSaving = true
      await api.updateSettings(formData)
      onRefresh()
      alert('Settings updated successfully!')
    } catch (err) {
      alert(`Failed to save settings: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSaving = false
    }
  }

  async function handleResetHealth() {
    try {
      isResetting = true
      await api.resetHealth(resetProvider)
      alert(`Health state and rate-limit locks for '${resetProvider}' have been reset.`)
    } catch (err) {
      alert(`Failed to reset health: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isResetting = false
    }
  }
</script>

<div class="p-6 max-w-4xl mx-auto space-y-6">
  <!-- Header -->
  <div class="flex items-center justify-between">
    <div>
      <h2 class="text-xl font-bold text-white tracking-tight">System & Proxy Settings</h2>
      <p class="text-xs text-slate-400">Configure gateway security, token saver engines, and failover health</p>
    </div>

    <button
      type="button"
      onclick={handleSave}
      disabled={isSaving}
      class="flex items-center gap-2 px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold transition shadow-lg shadow-indigo-600/20 cursor-pointer"
    >
      {#if isSaving}
        <Loader2 class="w-4 h-4 animate-spin" />
      {:else}
        <Save class="w-4 h-4" />
      {/if}
      <span>Save Changes</span>
    </button>
  </div>

  <!-- Security Section -->
  <div class="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
    <h3 class="text-sm font-bold text-white flex items-center gap-2">
      <Shield class="w-4 h-4 text-indigo-400" />
      <span>Security & Access Control</span>
    </h3>

    <div class="flex items-center justify-between p-4 rounded-2xl bg-slate-950/80 border border-slate-800">
      <div>
        <div class="text-xs font-bold text-white">Require Client API Key</div>
        <div class="text-[11px] text-slate-400">
          When enabled, incoming requests must supply a valid Bearer token from the API Keys table
        </div>
      </div>

      <label class="relative inline-flex items-center cursor-pointer">
        <input
          type="checkbox"
          checked={!!formData.requireApiKey}
          onchange={(e) => (formData.requireApiKey = e.currentTarget.checked)}
          class="sr-only peer"
        />
        <div class="w-11 h-6 bg-slate-800 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-indigo-600"></div>
      </label>
    </div>
  </div>

  <!-- Token Savers Section -->
  <div class="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
    <h3 class="text-sm font-bold text-white flex items-center gap-2">
      <Zap class="w-4 h-4 text-emerald-400" />
      <span>Token Saver Engines</span>
    </h3>

    <div class="space-y-3">
      <!-- RTK -->
      <div class="flex items-center justify-between p-4 rounded-2xl bg-slate-950/80 border border-slate-800">
        <div>
          <div class="text-xs font-bold text-white flex items-center gap-2">
            <span>RTK Compression</span>
            <span class="text-[9px] px-1.5 py-0.2 rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 font-mono">
              Recommended
            </span>
          </div>
          <div class="text-[11px] text-slate-400">
            Filters repetitive CLI, build, and git output to reduce prompt tokens by 60-80% without losing quality
          </div>
        </div>

        <label class="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={!!formData.rtkEnabled}
            onchange={(e) => (formData.rtkEnabled = e.currentTarget.checked)}
            class="sr-only peer"
          />
          <div class="w-11 h-6 bg-slate-800 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-emerald-600"></div>
        </label>
      </div>

      <!-- Caveman -->
      <div class="flex items-center justify-between p-4 rounded-2xl bg-slate-950/80 border border-slate-800">
        <div>
          <div class="text-xs font-bold text-white">Caveman Terse Output</div>
          <div class="text-[11px] text-slate-400">Instructs model to reply with ultra-succinct, non-hedging language</div>
        </div>

        <label class="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={!!formData.cavemanEnabled}
            onchange={(e) => (formData.cavemanEnabled = e.currentTarget.checked)}
            class="sr-only peer"
          />
          <div class="w-11 h-6 bg-slate-800 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-indigo-600"></div>
        </label>
      </div>

      <!-- Ponytail -->
      <div class="flex items-center justify-between p-4 rounded-2xl bg-slate-950/80 border border-slate-800">
        <div>
          <div class="text-xs font-bold text-white">Ponytail Code Style</div>
          <div class="text-[11px] text-slate-400">Enforces pragmatic, minimal boilerplate coding conventions</div>
        </div>

        <label class="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={!!formData.ponytailEnabled}
            onchange={(e) => (formData.ponytailEnabled = e.currentTarget.checked)}
            class="sr-only peer"
          />
          <div class="w-11 h-6 bg-slate-800 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-indigo-600"></div>
        </label>
      </div>
    </div>
  </div>

  <!-- Failover Health Cache Reset -->
  <div class="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
    <h3 class="text-sm font-bold text-white flex items-center gap-2">
      <RefreshCw class="w-4 h-4 text-indigo-400" />
      <span>Manual Health & Rate Limit Cache Reset</span>
    </h3>
    <p class="text-xs text-slate-400">
      If an account encountered HTTP 429 and was locked in cooldown, you can manually clear its lock here
    </p>

    <div class="flex items-center gap-3">
      <select
        bind:value={resetProvider}
        class="px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
      >
        <option value="antigravity">antigravity</option>
        <option value="freebuff">freebuff</option>
        <option value="clinepass">clinepass</option>
        <option value="deepseek">deepseek</option>
        <option value="groq">groq</option>
      </select>

      <button
        type="button"
        onclick={handleResetHealth}
        disabled={isResetting}
        class="px-4 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-white text-xs font-bold flex items-center gap-2 transition cursor-pointer"
      >
        {#if isResetting}
          <Loader2 class="w-3.5 h-3.5 animate-spin" />
        {:else}
          <Check class="w-3.5 h-3.5" />
        {/if}
        <span>Clear Rate Limit Lock</span>
      </button>
    </div>
  </div>
</div>
