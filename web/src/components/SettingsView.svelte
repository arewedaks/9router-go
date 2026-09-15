<script lang="ts">
  import {
    Check,
    Copy,
    Database,
    Download,
    Key,
    Loader2,
    Lock,
    RefreshCw,
    Save,
    Shield,
    Upload,
    Zap
  } from 'lucide-svelte'
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
      alert('System configuration and token savers updated successfully!')
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

<div class="space-y-6">
  <!-- Header (Stitch Screenshot) -->
  <div class="flex flex-col sm:flex-row sm:items-end justify-between gap-4">
    <div class="space-y-1.5">
      <div class="flex items-center gap-2">
        <span class="font-code text-[10px] uppercase tracking-wider text-[#ff5c35] px-2 py-0.5 rounded bg-[#ff5c35]/10 border border-[#ff5c35]/25 font-bold">
          System Control
        </span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-[#e1e2ea] tracking-tight">
        Local Mode & Gateway Configuration
      </h1>
      <p class="font-body text-xs sm:text-sm text-[#8e95a5] max-w-2xl leading-relaxed">
        Manage persistent SQLite state, master auth credentials, and load routing algorithms.
      </p>
    </div>

    <button
      type="button"
      onclick={handleSave}
      disabled={isSaving}
      class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-[#ff5c35] hover:brightness-110 text-white font-body text-xs font-bold shadow-md shadow-[#ff5c35]/25 transition cursor-pointer"
    >
      {#if isSaving}
        <Loader2 class="w-3.5 h-3.5 animate-spin" />
      {:else}
        <Save class="w-3.5 h-3.5" />
      {/if}
      <span>Save System State</span>
    </button>
  </div>

  <!-- 3 Main Configuration Cards (Stitch Design) -->
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
    <!-- Card 1: Local Machine Mode & Database -->
    <div class="p-6 rounded-xl bg-[#131722] border border-[#232a3b] space-y-4 shadow-xl flex flex-col justify-between">
      <div class="space-y-3">
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2">
            <Database class="w-4 h-4 text-[#4edea3]" />
            <h3 class="font-headline text-sm font-bold text-white">Local Machine Mode</h3>
          </div>
          <span class="font-code text-[10px] text-[#4edea3] bg-[#4edea3]/10 px-2 py-0.5 rounded border border-[#4edea3]/20">
            Running on :20130
          </span>
        </div>

        <div class="p-3 rounded-lg bg-[#0b0e13] border border-[#232a3b] space-y-1 font-code text-xs">
          <div class="text-[10px] text-[#8e95a5] uppercase">Database File Location</div>
          <div class="text-[#4cd7f6] font-semibold">~/.9router/db/data.sqlite</div>
          <div class="text-[10px] text-[#636c7e] pt-1">18.4 MB • SQLite WAL Mode • SetMaxOpenConns(4)</div>
        </div>
      </div>

      <div class="flex items-center gap-2 pt-2">
        <button
          type="button"
          class="flex-1 flex items-center justify-center gap-1.5 py-2 rounded-lg bg-[#1c2230] hover:bg-[#272f42] text-[#e1e2ea] font-body text-xs font-semibold border border-[#2b354a] transition cursor-pointer"
        >
          <Download class="w-3.5 h-3.5 text-[#4cd7f6]" />
          <span>Download Backup</span>
        </button>

        <button
          type="button"
          class="flex-1 flex items-center justify-center gap-1.5 py-2 rounded-lg bg-[#1c2230] hover:bg-[#272f42] text-[#e1e2ea] font-body text-xs font-semibold border border-[#2b354a] transition cursor-pointer"
        >
          <Upload class="w-3.5 h-3.5 text-[#ff8469]" />
          <span>Import Backup</span>
        </button>
      </div>
    </div>

    <!-- Card 2: Routing Strategy & Token Saver Engines -->
    <div class="p-6 rounded-xl bg-[#131722] border border-[#232a3b] space-y-4 shadow-xl">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <Zap class="w-4 h-4 text-[#ff5c35]" />
          <h3 class="font-headline text-sm font-bold text-white">Routing Strategy & Token Saver</h3>
        </div>
        <span class="font-code text-[10px] text-[#4cd7f6] bg-[#4cd7f6]/10 px-2 py-0.5 rounded border border-[#4cd7f6]/20">
          Active Engine
        </span>
      </div>

      <div class="space-y-3 font-body text-xs">
        <!-- RTK -->
        <div class="flex items-center justify-between p-3 rounded-lg bg-[#0b0e13] border border-[#232a3b]">
          <div>
            <div class="font-bold text-white flex items-center gap-1.5">
              <span>RTK Compression</span>
              <span class="text-[9px] px-1.5 py-0.2 rounded bg-[#4edea3]/15 text-[#4edea3] font-code">60-80% Savings</span>
            </div>
            <div class="text-[11px] text-[#8e95a5]">Filters repetitive CLI, build, and git output</div>
          </div>
          <input
            type="checkbox"
            checked={!!formData.rtkEnabled}
            onchange={(e) => (formData.rtkEnabled = e.currentTarget.checked)}
            class="w-4 h-4 accent-[#4edea3] cursor-pointer"
          />
        </div>

        <!-- Caveman -->
        <div class="flex items-center justify-between p-3 rounded-lg bg-[#0b0e13] border border-[#232a3b]">
          <div>
            <div class="font-bold text-white">Caveman Terse Output</div>
            <div class="text-[11px] text-[#8e95a5]">Instructs model to reply in concise, zero-filler language</div>
          </div>
          <input
            type="checkbox"
            checked={!!formData.cavemanEnabled}
            onchange={(e) => (formData.cavemanEnabled = e.currentTarget.checked)}
            class="w-4 h-4 accent-[#ff5c35] cursor-pointer"
          />
        </div>

        <!-- Ponytail -->
        <div class="flex items-center justify-between p-3 rounded-lg bg-[#0b0e13] border border-[#232a3b]">
          <div>
            <div class="font-bold text-white">Ponytail Code Style</div>
            <div class="text-[11px] text-[#8e95a5]">Enforces pragmatic, minimal boilerplate code style</div>
          </div>
          <input
            type="checkbox"
            checked={!!formData.ponytailEnabled}
            onchange={(e) => (formData.ponytailEnabled = e.currentTarget.checked)}
            class="w-4 h-4 accent-[#ff5c35] cursor-pointer"
          />
        </div>
      </div>
    </div>
  </div>

  <!-- Bottom Row: Security & Rate Limit Reset -->
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
    <!-- Security & Master Access -->
    <div class="p-6 rounded-xl bg-[#131722] border border-[#232a3b] space-y-4 shadow-xl">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <Shield class="w-4 h-4 text-[#4cd7f6]" />
          <h3 class="font-headline text-sm font-bold text-white">Security & Master Access</h3>
        </div>
        <div class="flex items-center gap-2 font-code text-xs">
          <span class="text-[#8e95a5]">Require Login:</span>
          <input
            type="checkbox"
            checked={!!formData.requireApiKey}
            onchange={(e) => (formData.requireApiKey = e.currentTarget.checked)}
            class="w-4 h-4 accent-[#ff5c35] cursor-pointer"
          />
        </div>
      </div>

      <p class="font-body text-xs text-[#8e95a5]">
        When enabled, client requests to <code class="font-code text-[#4cd7f6]">/v1/chat/completions</code> must provide a valid Bearer token from the CLI & Remote Access table.
      </p>
    </div>

    <!-- Health & Rate Limit Cache Reset -->
    <div class="p-6 rounded-xl bg-[#131722] border border-[#232a3b] space-y-4 shadow-xl">
      <div class="flex items-center gap-2">
        <RefreshCw class="w-4 h-4 text-[#ff8469]" />
        <h3 class="font-headline text-sm font-bold text-white">Failover Health Cache Reset</h3>
      </div>

      <p class="font-body text-xs text-[#8e95a5]">
        If an upstream credential hit HTTP 429 and was placed in cooldown, clear its lockout state manually:
      </p>

      <div class="flex items-center gap-2">
        <select
          bind:value={resetProvider}
          class="flex-1 px-3 py-2 rounded-lg bg-[#0b0e13] border border-[#232a3b] font-code text-xs text-white focus:outline-none focus:border-[#ff5c35]"
        >
          <option value="antigravity">antigravity (Google AI)</option>
          <option value="freebuff">freebuff (Codebuff)</option>
          <option value="clinepass">clinepass</option>
          <option value="deepseek">deepseek</option>
          <option value="groq">groq</option>
        </select>

        <button
          type="button"
          onclick={handleResetHealth}
          disabled={isResetting}
          class="px-4 py-2 rounded-lg bg-[#1c2230] hover:bg-[#272f42] text-white font-body text-xs font-bold flex items-center gap-1.5 transition cursor-pointer border border-[#2b354a]"
        >
          {#if isResetting}
            <Loader2 class="w-3.5 h-3.5 animate-spin text-[#4cd7f6]" />
          {:else}
            <Check class="w-3.5 h-3.5 text-[#4edea3]" />
          {/if}
          <span>Reset Cooldown</span>
        </button>
      </div>
    </div>
  </div>
</div>
