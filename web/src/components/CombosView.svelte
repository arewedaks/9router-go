<script lang="ts">
  import {
    ArrowDown,
    ArrowUp,
    Check,
    GitBranch,
    Layers,
    Loader2,
    Play,
    Plus,
    RefreshCw,
    Save,
    Trash2,
    Zap
  } from 'lucide-svelte'
  import { api, getAuthHeaders, type Combo } from '../api/client'

  let {
    combos = [],
    onRefresh
  }: {
    combos: Combo[]
    onRefresh: () => void
  } = $props()

  let selectedComboId = $state<string | null>(null)
  let isCreating = $state(false)
  let newComboName = $state('')
  let newComboStrategy = $state('fallback')

  let editingModels = $state<string[]>([])
  let editingStrategy = $state<string>('fallback')
  let modelInput = $state('')
  let isSaving = $state(false)

  let testPrompt = $state('Say hello in 3 words')
  let testOutput = $state('')
  let isTesting = $state(false)
  let testLatency = $state<number | null>(null)

  let selectedCombo = $derived(combos.find((c) => c.id === selectedComboId))

  $effect(() => {
    if (selectedComboId === null && combos.length > 0) {
      selectedComboId = combos[0].id
    }
    if (selectedCombo) {
      try {
        const parsed = JSON.parse(selectedCombo.models)
        editingModels = Array.isArray(parsed) ? parsed : [selectedCombo.models]
      } catch {
        editingModels = selectedCombo.models ? [selectedCombo.models] : []
      }
      editingStrategy = selectedCombo.strategy || 'fallback'
      testOutput = ''
      testLatency = null
    }
  })

  function handleMoveModel(index: number, delta: number) {
    const newIdx = index + delta
    if (newIdx < 0 || newIdx >= editingModels.length) return
    const updated = [...editingModels]
    const temp = updated[index]
    updated[index] = updated[newIdx]
    updated[newIdx] = temp
    editingModels = updated
  }

  function handleRemoveModel(index: number) {
    editingModels = editingModels.filter((_, i) => i !== index)
  }

  function handleAddModel() {
    if (!modelInput.trim()) return
    editingModels = [...editingModels, modelInput.trim()]
    modelInput = ''
  }

  async function handleSaveCombo() {
    if (!selectedCombo) return
    try {
      isSaving = true
      await api.updateCombo(selectedCombo.id, {
        models: JSON.stringify(editingModels),
        strategy: editingStrategy,
      })
      onRefresh()
      alert('Combo pipeline saved successfully!')
    } catch (err) {
      alert(`Failed to save combo: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSaving = false
    }
  }

  async function handleCreateCombo(e: SubmitEvent) {
    e.preventDefault()
    if (!newComboName.trim()) return
    try {
      isSaving = true
      await api.createCombo({
        name: newComboName.trim(),
        models: JSON.stringify([]),
        strategy: newComboStrategy,
      })
      isCreating = false
      newComboName = ''
      onRefresh()
    } catch (err) {
      alert(`Failed to create combo: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      isSaving = false
    }
  }

  async function handleDeleteCombo(id: string) {
    if (!confirm('Are you sure you want to delete this combo?')) return
    try {
      await api.deleteCombo(id)
      onRefresh()
      selectedComboId = combos.find((c) => c.id !== id)?.id || null
    } catch (err) {
      alert(`Failed to delete combo: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  async function handleRunLiveTest() {
    if (!selectedCombo) return
    isTesting = true
    testOutput = ''
    testLatency = null
    const startTime = performance.now()

    try {
      const res = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers: getAuthHeaders(),
        body: JSON.stringify({
          model: selectedCombo.name,
          messages: [{ role: 'user', content: testPrompt }],
          stream: true,
          max_tokens: 150,
        }),
      })

      if (!res.ok) {
        const errText = await res.text()
        throw new Error(errText || `HTTP ${res.status}`)
      }

      const reader = res.body?.getReader()
      const decoder = new TextDecoder()
      if (!reader) return

      let buffer = ''
      while (true) {
        const { done, value } = await reader.read()
        if (done) break

        buffer += decoder.decode(value, { stream: true })
        const lines = buffer.split('\n')
        buffer = lines.pop() || ''

        for (const line of lines) {
          const trimmed = line.trim()
          if (!trimmed || trimmed.startsWith(':')) continue
          if (trimmed === 'data: [DONE]') continue
          if (trimmed.startsWith('data: ')) {
            try {
              const chunk = JSON.parse(trimmed.slice(6))
              const delta = chunk.choices?.[0]?.delta?.content || ''
              const reasoning = chunk.choices?.[0]?.delta?.reasoning_content || ''
              if (reasoning) {
                testOutput += `[thinking: ${reasoning}]`
              }
              if (delta) {
                testOutput += delta
              }
            } catch {
              // ignore
            }
          }
        }
      }
      testLatency = Math.round(performance.now() - startTime)
    } catch (err) {
      testOutput = `Error: ${err instanceof Error ? err.message : String(err)}`
    } finally {
      isTesting = false
    }
  }
</script>

<div class="p-4 sm:p-6 lg:p-8 max-w-[1560px] mx-auto space-y-6">
  <!-- Header Section & Operational Status -->
  <div class="flex flex-col md:flex-row md:items-end justify-between gap-4">
    <div class="space-y-1.5 max-w-2xl">
      <div class="flex items-center gap-2">
        <span class="font-code text-[11px] uppercase tracking-wider text-primary-container px-2 py-0.5 rounded bg-primary-container/10 border border-primary-container/20 font-bold">
          Virtualization Layer
        </span>
        <span class="text-outline">•</span>
        <span class="font-code text-[11px] text-outline">Cluster 9Router-East</span>
      </div>
      <h1 class="font-headline text-2xl sm:text-3xl font-bold text-on-surface tracking-tight">
        Model Combos & Intelligent Routing
      </h1>
      <p class="font-body text-xs sm:text-sm text-on-surface-variant leading-relaxed">
        Group multiple LLMs under unified virtual endpoints with automated failover, load balancing, or consensus fusion across multi-cloud credentials.
      </p>
    </div>

    <!-- Quick Metrics Summary Pills -->
    <div class="flex flex-wrap items-center gap-2">
      <div class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-surface-container-low border border-surface-container-high font-code text-xs">
        <span class="text-outline">Active Combos:</span>
        <span class="text-secondary font-bold">{combos.length}</span>
      </div>

      <button
        type="button"
        onclick={() => (isCreating = true)}
        class="flex items-center gap-1.5 px-3.5 py-2 rounded-lg bg-primary-container hover:brightness-110 text-on-primary font-body text-xs font-bold shadow-md shadow-primary-container/25 transition cursor-pointer"
      >
        <Plus class="w-4 h-4" />
        <span>Create New Combo</span>
      </button>
    </div>
  </div>

  <!-- Strategy Blueprint Cards (From Stitch Design) -->
  <div class="grid grid-cols-1 md:grid-cols-3 gap-3">
    <!-- Fallback Chain -->
    <div class="p-4 rounded-xl bg-surface-container-low border border-surface-container-high space-y-2">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <div class="p-1.5 rounded-lg bg-primary-container/15 text-primary-container">
            <GitBranch class="w-4 h-4" />
          </div>
          <span class="font-headline text-xs font-bold text-on-surface">Fallback Chain</span>
        </div>
        <span class="font-code text-[10px] text-primary-container bg-primary-container/10 px-2 py-0.5 rounded-full border border-primary-container/20">
          Default
        </span>
      </div>
      <p class="font-body text-[11px] text-on-surface-variant leading-relaxed">
        Queries models sequentially. If primary model returns 429, 5xx, or timeouts, seamlessly switches downstream without socket drops.
      </p>
      <div class="font-code text-[10px] text-outline flex items-center gap-1.5 pt-1">
        <span class="w-1.5 h-1.5 rounded-full bg-primary-container"></span>
        <span>Policy: Next-on-failure</span>
      </div>
    </div>

    <!-- Round Robin -->
    <div class="p-4 rounded-xl bg-surface-container-low border border-surface-container-high space-y-2">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <div class="p-1.5 rounded-lg bg-secondary/15 text-secondary">
            <RefreshCw class="w-4 h-4" />
          </div>
          <span class="font-headline text-xs font-bold text-on-surface">Round Robin</span>
        </div>
        <span class="font-code text-[10px] text-secondary bg-secondary/10 px-2 py-0.5 rounded-full border border-secondary/20">
          Load Spread
        </span>
      </div>
      <p class="font-body text-[11px] text-on-surface-variant leading-relaxed">
        Rotates requests across candidate keys and regional instances to maximize TPM quotas and minimize rate-limit throttling.
      </p>
      <div class="font-code text-[10px] text-outline flex items-center gap-1.5 pt-1">
        <span class="w-1.5 h-1.5 rounded-full bg-secondary"></span>
        <span>Policy: Weighted distribution</span>
      </div>
    </div>

    <!-- Consensus Fusion -->
    <div class="p-4 rounded-xl bg-surface-container-low border border-surface-container-high space-y-2">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <div class="p-1.5 rounded-lg bg-tertiary/15 text-tertiary">
            <Zap class="w-4 h-4" />
          </div>
          <span class="font-headline text-xs font-bold text-on-surface">Consensus Fusion</span>
        </div>
        <span class="font-code text-[10px] text-tertiary bg-tertiary/10 px-2 py-0.5 rounded-full border border-tertiary/20">
          Max Quality
        </span>
      </div>
      <p class="font-body text-[11px] text-on-surface-variant leading-relaxed">
        Queries parallel LLM nodes simultaneously, using fast-evaluator judge to select or synthesize the most coherent response.
      </p>
      <div class="font-code text-[10px] text-outline flex items-center gap-1.5 pt-1">
        <span class="w-1.5 h-1.5 rounded-full bg-tertiary"></span>
        <span>Policy: Parallel Judge</span>
      </div>
    </div>
  </div>

  <!-- Main Work Area: Combos List & Editor -->
  <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
    <!-- Left Column: Combo Cards -->
    <div class="space-y-2">
      <div class="font-headline text-xs font-bold text-on-surface-variant uppercase tracking-wider px-1">
        Virtual Endpoints ({combos.length})
      </div>

      {#each combos as c (c.id)}
        {@const isSelected = c.id === selectedComboId}
        {@const count = (() => {
          try {
            return JSON.parse(c.models).length
          } catch {
            return c.models ? 1 : 0
          }
        })()}

        <div
          role="button"
          tabindex="0"
          onclick={() => (selectedComboId = c.id)}
          onkeydown={(e) => e.key === 'Enter' && (selectedComboId = c.id)}
          class="p-4 rounded-xl border cursor-pointer transition-all flex items-center justify-between {isSelected
            ? 'bg-surface-container border-primary-container/50 shadow-md shadow-primary-container/10'
            : 'bg-surface-container-low border-surface-container-high hover:border-surface-container-highest'}"
        >
          <div class="flex items-center gap-3">
            <div class="w-8 h-8 rounded-lg bg-primary-container/10 text-primary-container border border-primary-container/20 flex items-center justify-center font-bold text-xs">
              <Layers class="w-4 h-4" />
            </div>
            <div>
              <h4 class="font-headline text-xs font-bold text-on-surface">{c.name}</h4>
              <p class="font-code text-[10px] text-outline">{count} fallback models</p>
            </div>
          </div>

          <span class="font-code text-[10px] px-2 py-0.5 rounded bg-surface-container-lowest text-secondary border border-surface-container-high capitalize">
            {c.strategy || 'fallback'}
          </span>
        </div>
      {/each}
    </div>

    <!-- Right Column: Combo Pipeline Editor & Playground -->
    <div class="lg:col-span-2 space-y-6">
      {#if selectedCombo}
        <div class="bg-surface-container-low border border-surface-container-high rounded-2xl p-6 space-y-6 shadow-xl">
          <!-- Editor Header -->
          <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-3 border-b border-surface-container pb-4">
            <div>
              <div class="flex items-center gap-2">
                <h3 class="font-headline text-base font-bold text-on-surface">{selectedCombo.name}</h3>
                <span class="font-code text-[10px] px-2 py-0.5 rounded-full bg-primary-container/15 text-primary border border-primary-container/30">
                  model: "{selectedCombo.name}"
                </span>
              </div>
              <p class="font-body text-xs text-on-surface-variant">
                Ordered fallback pipeline executed by 9Router gateway
              </p>
            </div>

            <div class="flex items-center gap-2 self-end sm:self-auto">
              <button
                type="button"
                onclick={() => handleDeleteCombo(selectedCombo.id)}
                class="p-2 rounded-lg text-outline hover:text-error hover:bg-error-container/20 transition cursor-pointer"
                title="Delete Combo"
              >
                <Trash2 class="w-4 h-4" />
              </button>
              <button
                type="button"
                onclick={handleSaveCombo}
                disabled={isSaving}
                class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-tertiary-container hover:brightness-110 text-on-tertiary font-body text-xs font-bold transition shadow-md shadow-tertiary-container/20 cursor-pointer"
              >
                {#if isSaving}
                  <Loader2 class="w-4 h-4 animate-spin" />
                {:else}
                  <Save class="w-4 h-4" />
                {/if}
                <span>Save Pipeline</span>
              </button>
            </div>
          </div>

          <!-- Strategy Selector -->
          <div>
            <div class="font-body text-xs font-semibold text-on-surface-variant mb-2">Routing Strategy</div>
            <div class="grid grid-cols-2 gap-3 max-w-sm">
              <button
                type="button"
                onclick={() => (editingStrategy = 'fallback')}
                class="p-2.5 rounded-xl border text-xs font-bold transition cursor-pointer {editingStrategy === 'fallback'
                  ? 'bg-primary-container border-primary-container text-on-primary'
                  : 'bg-surface-container border-surface-container-high text-on-surface-variant'}"
              >
                Sequential Fallback
              </button>
              <button
                type="button"
                onclick={() => (editingStrategy = 'round-robin')}
                class="p-2.5 rounded-xl border text-xs font-bold transition cursor-pointer {editingStrategy === 'round-robin'
                  ? 'bg-primary-container border-primary-container text-on-primary'
                  : 'bg-surface-container border-surface-container-high text-on-surface-variant'}"
              >
                Round Robin
              </button>
            </div>
          </div>

          <!-- Fallback Models Sequence -->
          <div class="space-y-3">
            <div class="font-body text-xs font-semibold text-on-surface-variant">
              Model Priority Pipeline (Executed top-to-bottom)
            </div>

            <div class="space-y-2">
              {#each editingModels as model, idx}
                <div class="flex items-center justify-between p-3 rounded-xl bg-surface-container border border-surface-container-high">
                  <div class="flex items-center gap-3">
                    <span class="w-6 h-6 rounded bg-surface-container-highest text-on-surface flex items-center justify-center font-bold text-xs font-code">
                      {idx + 1}
                    </span>
                    <span class="font-code text-xs text-on-surface font-medium">{model}</span>
                  </div>

                  <div class="flex items-center gap-1">
                    <button
                      type="button"
                      onclick={() => handleMoveModel(idx, -1)}
                      disabled={idx === 0}
                      class="p-1 rounded text-outline hover:text-on-surface disabled:opacity-30 cursor-pointer"
                    >
                      <ArrowUp class="w-3.5 h-3.5" />
                    </button>
                    <button
                      type="button"
                      onclick={() => handleMoveModel(idx, 1)}
                      disabled={idx === editingModels.length - 1}
                      class="p-1 rounded text-outline hover:text-on-surface disabled:opacity-30 cursor-pointer"
                    >
                      <ArrowDown class="w-3.5 h-3.5" />
                    </button>
                    <button
                      type="button"
                      onclick={() => handleRemoveModel(idx)}
                      class="p-1 rounded text-outline hover:text-error ml-2 cursor-pointer"
                    >
                      <Trash2 class="w-3.5 h-3.5" />
                    </button>
                  </div>
                </div>
              {/each}
            </div>

            <!-- Add model row -->
            <div class="flex gap-2 pt-2">
              <input
                type="text"
                placeholder="Enter model identifier (e.g. fb/z-ai/glm-5.3-flash, ag/gemini-2.5-flash)"
                bind:value={modelInput}
                onkeydown={(e) => e.key === 'Enter' && handleAddModel()}
                class="flex-1 px-3 py-2 rounded-xl bg-surface-container border border-surface-container-high font-code text-xs text-on-surface focus:outline-none focus:border-primary-container"
              />
              <button
                type="button"
                onclick={handleAddModel}
                class="px-4 py-2 rounded-xl bg-surface-container-high hover:bg-surface-container-highest text-on-surface font-body text-xs font-bold flex items-center gap-1.5 transition cursor-pointer"
              >
                <Plus class="w-3.5 h-3.5" />
                <span>Add Model</span>
              </button>
            </div>
          </div>

          <!-- Live Test Playground (Stitch Style) -->
          <div class="pt-4 border-t border-surface-container space-y-3">
            <div class="flex items-center justify-between">
              <h4 class="font-headline text-xs font-bold text-on-surface flex items-center gap-2">
                <Play class="w-3.5 h-3.5 text-tertiary" />
                <span>Live Test Playground</span>
              </h4>
              {#if testLatency !== null}
                <span class="font-code text-[10px] text-tertiary font-semibold">RTT: {testLatency}ms</span>
              {/if}
            </div>

            <div class="flex gap-2">
              <input
                type="text"
                bind:value={testPrompt}
                placeholder="Test prompt..."
                class="flex-1 px-3 py-2 rounded-xl bg-surface-container border border-surface-container-high font-body text-xs text-on-surface focus:outline-none focus:border-primary-container"
              />
              <button
                type="button"
                onclick={handleRunLiveTest}
                disabled={isTesting}
                class="px-4 py-2 rounded-xl bg-primary-container hover:brightness-110 text-on-primary font-body text-xs font-bold flex items-center gap-1.5 transition cursor-pointer"
              >
                {#if isTesting}
                  <Loader2 class="w-3.5 h-3.5 animate-spin" />
                {:else}
                  <Play class="w-3.5 h-3.5" />
                {/if}
                <span>Run Test</span>
              </button>
            </div>

            {#if testOutput}
              <div class="p-3.5 rounded-xl bg-surface-container-lowest border border-surface-container-high font-code text-xs text-tertiary whitespace-pre-wrap max-h-48 overflow-y-auto">
                {testOutput}
              </div>
            {/if}
          </div>
        </div>
      {:else}
        <div class="p-12 text-center text-outline text-xs border border-dashed border-surface-container-high rounded-2xl">
          Select a combo to view details and edit pipeline
        </div>
      {/if}
    </div>
  </div>

  <!-- New Combo Modal (Mac-Style Window) -->
  {#if isCreating}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-surface-container-lowest/80 backdrop-blur-md p-4">
      <div class="w-full max-w-md p-6 rounded-2xl bg-surface-container-high border border-surface-container-highest shadow-2xl space-y-4">
        <div class="flex items-center justify-between pb-2 border-b border-surface-container">
          <div class="flex items-center gap-2">
            <button
              type="button"
              aria-label="Close dialog"
              onclick={() => (isCreating = false)}
              class="w-3 h-3 rounded-full bg-error cursor-pointer"
            ></button>
            <div class="w-3 h-3 rounded-full bg-outline"></div>
            <div class="w-3 h-3 rounded-full bg-tertiary"></div>
            <span class="ml-2 font-headline text-sm font-bold text-on-surface">
              Create Virtual Model Combo
            </span>
          </div>
        </div>

        <form onsubmit={handleCreateCombo} class="space-y-3 font-body text-xs">
          <div>
            <label for="combo-name" class="block font-semibold text-on-surface-variant mb-1">Combo Name *</label>
            <input
              id="combo-name"
              type="text"
              placeholder="e.g. smart-coder-pro"
              bind:value={newComboName}
              required
              class="w-full bg-surface-container border border-surface-container-high rounded-lg px-3 py-2 font-code text-xs text-on-surface focus:outline-none focus:ring-1 focus:ring-primary-container"
            />
          </div>

          <div>
            <label for="strategy-select" class="block font-semibold text-on-surface-variant mb-1">Routing Strategy</label>
            <select
              id="strategy-select"
              bind:value={newComboStrategy}
              class="w-full bg-surface-container border border-surface-container-high rounded-lg px-3 py-2 font-code text-xs text-on-surface focus:outline-none focus:ring-1 focus:ring-primary-container"
            >
              <option value="fallback">Sequential Fallback (Recommended)</option>
              <option value="round-robin">Round Robin</option>
            </select>
          </div>

          <div class="flex justify-end gap-2 pt-3 border-t border-surface-container">
            <button
              type="button"
              onclick={() => (isCreating = false)}
              class="px-4 py-2 rounded-lg text-on-surface-variant hover:text-on-surface cursor-pointer"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isSaving}
              class="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-primary-container hover:brightness-110 text-on-primary font-bold shadow-md shadow-primary-container/20 cursor-pointer"
            >
              <Check class="w-3.5 h-3.5" />
              <span>Create Combo</span>
            </button>
          </div>
        </form>
      </div>
    </div>
  {/if}
</div>
