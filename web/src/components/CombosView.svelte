<script lang="ts">
  import {
    ArrowDown,
    ArrowUp,
    Check,
    Layers,
    Loader2,
    Play,
    Plus,
    Save,
    Trash2
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
      alert('Combo saved successfully!')
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
    } catch (err) {
      testOutput = `Error: ${err instanceof Error ? err.message : String(err)}`
    } finally {
      isTesting = false
    }
  }
</script>

<div class="p-6 max-w-7xl mx-auto space-y-6">
  <!-- Header -->
  <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
    <div>
      <h2 class="text-xl font-bold text-white tracking-tight flex items-center gap-2">
        <span>Combos & Smart Routing</span>
        <span class="text-xs font-semibold px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 border border-slate-700">
          {combos.length} combos
        </span>
      </h2>
      <p class="text-xs text-slate-400">
        Combine multiple models into unified aliases with automated failover and round-robin
      </p>
    </div>

    <button
      type="button"
      onclick={() => (isCreating = true)}
      class="flex items-center gap-2 px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold transition shadow-lg shadow-indigo-600/20 cursor-pointer"
    >
      <Plus class="w-4 h-4" />
      <span>New Combo</span>
    </button>
  </div>

  <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
    <!-- Left: Combo List -->
    <div class="space-y-2">
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
          class="p-4 rounded-2xl border cursor-pointer transition-all flex items-center justify-between {isSelected
            ? 'bg-slate-900 border-indigo-500/50 shadow-md shadow-indigo-500/10'
            : 'bg-slate-900/50 border-slate-800/80 hover:border-slate-700'}"
        >
          <div class="flex items-center gap-3">
            <div class="w-8 h-8 rounded-xl bg-indigo-500/10 text-indigo-400 border border-indigo-500/20 flex items-center justify-center font-bold text-xs">
              <Layers class="w-4 h-4" />
            </div>
            <div>
              <h4 class="text-xs font-bold text-white">{c.name}</h4>
              <p class="text-[11px] text-slate-400">{count} fallback models</p>
            </div>
          </div>

          <div class="flex items-center gap-2">
            <span class="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-950 text-slate-400 border border-slate-800 capitalize">
              {c.strategy || 'fallback'}
            </span>
          </div>
        </div>
      {/each}
    </div>

    <!-- Right: Combo Editor & Playground -->
    <div class="lg:col-span-2 space-y-6">
      {#if selectedCombo}
        <div class="bg-slate-900/70 border border-slate-800 rounded-3xl p-6 space-y-6">
          <div class="flex items-center justify-between border-b border-slate-800 pb-4">
            <div>
              <div class="flex items-center gap-2">
                <h3 class="text-base font-bold text-white">{selectedCombo.name}</h3>
                <span class="text-[10px] px-2 py-0.5 rounded-full bg-indigo-500/10 text-indigo-400 border border-indigo-500/20 font-mono">
                  model: "{selectedCombo.name}"
                </span>
              </div>
              <p class="text-xs text-slate-400">Ordered fallback pipeline executed by 9router-go</p>
            </div>

            <div class="flex items-center gap-2">
              <button
                type="button"
                onclick={() => handleDeleteCombo(selectedCombo.id)}
                class="p-2 rounded-xl text-slate-400 hover:text-rose-400 hover:bg-rose-500/10 transition cursor-pointer"
                title="Delete Combo"
              >
                <Trash2 class="w-4 h-4" />
              </button>
              <button
                type="button"
                onclick={handleSaveCombo}
                disabled={isSaving}
                class="flex items-center gap-2 px-4 py-2 rounded-xl bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-bold transition shadow-lg shadow-emerald-600/20 cursor-pointer"
              >
                {#if isSaving}
                  <Loader2 class="w-4 h-4 animate-spin" />
                {:else}
                  <Save class="w-4 h-4" />
                {/if}
                <span>Save Combo</span>
              </button>
            </div>
          </div>

          <!-- Strategy selector -->
          <div>
            <div class="block text-xs font-semibold text-slate-300 mb-1.5">Routing Strategy</div>
            <div class="grid grid-cols-2 gap-3 max-w-sm">
              <button
                type="button"
                onclick={() => (editingStrategy = 'fallback')}
                class="p-2.5 rounded-xl border text-xs font-bold transition cursor-pointer {editingStrategy === 'fallback'
                  ? 'bg-indigo-600 border-indigo-500 text-white'
                  : 'bg-slate-950 border-slate-800 text-slate-400'}"
              >
                Sequential Fallback
              </button>
              <button
                type="button"
                onclick={() => (editingStrategy = 'round-robin')}
                class="p-2.5 rounded-xl border text-xs font-bold transition cursor-pointer {editingStrategy === 'round-robin'
                  ? 'bg-indigo-600 border-indigo-500 text-white'
                  : 'bg-slate-950 border-slate-800 text-slate-400'}"
              >
                Round Robin
              </button>
            </div>
          </div>

          <!-- Fallback models sequence -->
          <div class="space-y-3">
            <div class="block text-xs font-semibold text-slate-300">
              Model Priority Pipeline (Executed top-to-bottom)
            </div>

            <div class="space-y-2">
              {#each editingModels as model, idx}
                <div
                  class="flex items-center justify-between p-3 rounded-xl bg-slate-950/80 border border-slate-800"
                >
                  <div class="flex items-center gap-3">
                    <span class="w-6 h-6 rounded-lg bg-slate-800 text-slate-300 flex items-center justify-center font-bold text-xs font-mono">
                      {idx + 1}
                    </span>
                    <span class="text-xs text-slate-200 font-mono font-medium">{model}</span>
                  </div>

                  <div class="flex items-center gap-1">
                    <button
                      type="button"
                      onclick={() => handleMoveModel(idx, -1)}
                      disabled={idx === 0}
                      class="p-1 rounded text-slate-400 hover:text-white disabled:opacity-30 cursor-pointer"
                    >
                      <ArrowUp class="w-3.5 h-3.5" />
                    </button>
                    <button
                      type="button"
                      onclick={() => handleMoveModel(idx, 1)}
                      disabled={idx === editingModels.length - 1}
                      class="p-1 rounded text-slate-400 hover:text-white disabled:opacity-30 cursor-pointer"
                    >
                      <ArrowDown class="w-3.5 h-3.5" />
                    </button>
                    <button
                      type="button"
                      onclick={() => handleRemoveModel(idx)}
                      class="p-1 rounded text-slate-400 hover:text-rose-400 ml-2 cursor-pointer"
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
                placeholder="Enter model string (e.g. fb/z-ai/glm-5.3-flash, ag/gemini-3.8-flash-high)"
                bind:value={modelInput}
                onkeydown={(e) => e.key === 'Enter' && handleAddModel()}
                class="flex-1 px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
              />
              <button
                type="button"
                onclick={handleAddModel}
                class="px-4 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-white text-xs font-bold flex items-center gap-1.5 transition cursor-pointer"
              >
                <Plus class="w-3.5 h-3.5" />
                <span>Add</span>
              </button>
            </div>
          </div>

          <!-- Live Test Playground -->
          <div class="pt-4 border-t border-slate-800 space-y-3">
            <h4 class="text-xs font-bold text-white flex items-center gap-2">
              <Play class="w-3.5 h-3.5 text-emerald-400" />
              <span>Live Test Playground</span>
            </h4>

            <div class="flex gap-2">
              <input
                type="text"
                bind:value={testPrompt}
                placeholder="Test prompt..."
                class="flex-1 px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
              />
              <button
                type="button"
                onclick={handleRunLiveTest}
                disabled={isTesting}
                class="px-4 py-2 rounded-xl bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-bold flex items-center gap-1.5 transition cursor-pointer"
              >
                {#if isTesting}
                  <Loader2 class="w-3.5 h-3.5 animate-spin" />
                {:else}
                  <Play class="w-3.5 h-3.5" />
                {/if}
                <span>Test Run</span>
              </button>
            </div>

            {#if testOutput}
              <div class="p-3.5 rounded-xl bg-slate-950 border border-slate-800 text-xs font-mono text-emerald-400 whitespace-pre-wrap max-h-48 overflow-y-auto">
                {testOutput}
              </div>
            {/if}
          </div>
        </div>
      {:else}
        <div class="p-12 text-center text-slate-500 text-xs border border-dashed border-slate-800 rounded-3xl">
          Select a combo to view details and edit pipeline
        </div>
      {/if}
    </div>
  </div>

  <!-- New Combo Modal -->
  {#if isCreating}
    <div class="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-center justify-center p-4">
      <div class="bg-slate-900 border border-slate-800 rounded-3xl w-full max-w-md p-6 shadow-2xl space-y-4">
        <h3 class="text-base font-bold text-white flex items-center gap-2">
          <Plus class="w-4 h-4 text-indigo-400" />
          <span>Create New Combo</span>
        </h3>

        <form onsubmit={handleCreateCombo} class="space-y-4">
          <div>
            <label for="combo-name-input" class="block text-xs font-semibold text-slate-300 mb-1">Combo Name *</label>
            <input
              id="combo-name-input"
              type="text"
              placeholder="e.g. my-coding-combo"
              bind:value={newComboName}
              required
              class="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
            />
          </div>

          <div>
            <label for="combo-strat-select" class="block text-xs font-semibold text-slate-300 mb-1">Routing Strategy</label>
            <select
              id="combo-strat-select"
              bind:value={newComboStrategy}
              class="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
            >
              <option value="fallback">Sequential Fallback</option>
              <option value="round-robin">Round Robin</option>
            </select>
          </div>

          <div class="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onclick={() => (isCreating = false)}
              class="px-4 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-300 text-xs font-bold cursor-pointer"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isSaving}
              class="px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold flex items-center gap-1.5 cursor-pointer"
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
