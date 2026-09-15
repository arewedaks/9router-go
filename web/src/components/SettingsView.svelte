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

<div class="p-4 sm:p-6 lg:p-8 max-w-4xl mx-auto space-y-6">
  <!-- Header -->
  <div class="flex items-center justify-between">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[11px] uppercase tracking-wider text-primary-container px-2 py-0.5 rounded bg-primary-container/10 border border-primary-container/20 font-bold">
          System Control
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-on-surface tracking-tight">
        Gateway & Security Settings
      </h1>
      <p class="font-body text-xs sm:text-sm text-on-surface-variant leading-relaxed">
        Configure gateway security, client authorization, token saving engines, and failover health caches.
      </p>
    </div>

    <button
      type="button"
      onclick={handleSave}
      disabled={isSaving}
      class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-primary-container hover:brightness-110 text-on-primary font-body text-xs font-bold shadow-md shadow-primary-container/25 transition cursor-pointer"
    >
      {#if isSaving}
        <Loader2 class="w-3.5 h-3.5 animate-spin" />
      {:else}
        <Save class="w-3.5 h-3.5" />
      {/if}
      <span>Save Settings</span>
    </button>
  </div>

  <!-- Security Section -->
  <div class="bg-surface-container-low border border-surface-container-high rounded-2xl p-6 shadow-xl space-y-4">
    <h3 class="font-headline text-sm font-bold text-on-surface flex items-center gap-2">
      <Shield class="w-4 h-4 text-primary-container" />
      <span>Security & Access Control</span>
    </h3>

    <div class="flex items-center justify-between p-4 rounded-xl bg-surface-container border border-surface-container-high">
      <div>
        <div class="font-body text-xs font-bold text-on-surface">Require Client API Key</div>
        <div class="font-body text-[11px] text-on-surface-variant">
          When enabled, incoming client requests must supply a valid Bearer token from the API Keys table
        </div>
      </div>

      <label class="relative inline-flex items-center cursor-pointer">
        <input
          type="checkbox"
          checked={!!formData.requireApiKey}
          onchange={(e) => (formData.requireApiKey = e.currentTarget.checked)}
          class="sr-only peer"
        />
        <div class="w-11 h-6 bg-surface-container-highest peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary-container"></div>
      </label>
    </div>
  </div>

  <!-- Token Savers Section (From Stitch Design) -->
  <div class="bg-surface-container-low border border-surface-container-high rounded-2xl p-6 shadow-xl space-y-4">
    <h3 class="font-headline text-sm font-bold text-on-surface flex items-center gap-2">
      <Zap class="w-4 h-4 text-tertiary" />
      <span>Token Saver Engines</span>
    </h3>

    <div class="space-y-3">
      <!-- RTK -->
      <div class="flex items-center justify-between p-4 rounded-xl bg-surface-container border border-surface-container-high">
        <div>
          <div class="font-body text-xs font-bold text-on-surface flex items-center gap-2">
            <span>RTK Token Filter</span>
            <span class="font-code text-[9px] px-1.5 py-0.2 rounded bg-tertiary/10 text-tertiary border border-tertiary/20">
              Recommended (60-80% savings)
            </span>
          </div>
          <div class="font-body text-[11px] text-on-surface-variant">
            Filters repetitive CLI, build, test, and git output without losing code context or model instruction quality
          </div>
        </div>

        <label class="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={!!formData.rtkEnabled}
            onchange={(e) => (formData.rtkEnabled = e.currentTarget.checked)}
            class="sr-only peer"
          />
          <div class="w-11 h-6 bg-surface-container-highest peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-tertiary-container"></div>
        </label>
      </div>

      <!-- Caveman -->
      <div class="flex items-center justify-between p-4 rounded-xl bg-surface-container border border-surface-container-high">
        <div>
          <div class="font-body text-xs font-bold text-on-surface">Caveman Terse Mode</div>
          <div class="font-body text-[11px] text-on-surface-variant">
            Instructs model to reply in ultra-succinct, non-hedging language to conserve completion tokens
          </div>
        </div>

        <label class="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={!!formData.cavemanEnabled}
            onchange={(e) => (formData.cavemanEnabled = e.currentTarget.checked)}
            class="sr-only peer"
          />
          <div class="w-11 h-6 bg-surface-container-highest peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary-container"></div>
        </label>
      </div>

      <!-- Ponytail -->
      <div class="flex items-center justify-between p-4 rounded-xl bg-surface-container border border-surface-container-high">
        <div>
          <div class="font-body text-xs font-bold text-on-surface">Ponytail Code Style</div>
          <div class="font-body text-[11px] text-on-surface-variant">
            Enforces pragmatic, minimal boilerplate clean code conventions across coding turns
          </div>
        </div>

        <label class="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={!!formData.ponytailEnabled}
            onchange={(e) => (formData.ponytailEnabled = e.currentTarget.checked)}
            class="sr-only peer"
          />
          <div class="w-11 h-6 bg-surface-container-highest peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-primary-container"></div>
        </label>
      </div>
    </div>
  </div>

  <!-- Failover Health Cache Reset -->
  <div class="bg-surface-container-low border border-surface-container-high rounded-2xl p-6 shadow-xl space-y-4">
    <h3 class="font-headline text-sm font-bold text-on-surface flex items-center gap-2">
      <RefreshCw class="w-4 h-4 text-secondary" />
      <span>Manual Health & Rate Limit Cache Reset</span>
    </h3>
    <p class="font-body text-xs text-on-surface-variant">
      If an account encountered HTTP 429 and was temporarily locked in cooldown, you can manually clear its lockout state here.
    </p>

    <div class="flex items-center gap-3">
      <select
        bind:value={resetProvider}
        class="px-3 py-2 rounded-lg bg-surface-container border border-surface-container-high font-code text-xs text-on-surface focus:outline-none focus:border-primary-container"
      >
        <option value="antigravity">antigravity (Google AI)</option>
        <option value="freebuff">freebuff (Codebuff)</option>
        <option value="clinepass">clinepass (Cline Pass)</option>
        <option value="deepseek">deepseek</option>
        <option value="groq">groq</option>
      </select>

      <button
        type="button"
        onclick={handleResetHealth}
        disabled={isResetting}
        class="px-4 py-2 rounded-lg bg-surface-container-high hover:bg-surface-container-highest text-on-surface font-body text-xs font-bold flex items-center gap-2 transition cursor-pointer border border-surface-container-highest"
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
