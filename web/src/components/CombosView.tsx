import React, { useState } from 'react'
import {
  ArrowDown,
  ArrowUp,
  Check,
  Layers,
  Loader2,
  Play,
  Plus,
  Save,
  Trash2,
} from 'lucide-react'
import { api, getAuthHeaders, type Combo } from '../api/client'

interface CombosViewProps {
  combos: Combo[]
  onRefresh: () => void
}

export const CombosView: React.FC<CombosViewProps> = ({ combos, onRefresh }) => {
  const [selectedComboId, setSelectedComboId] = useState<string | null>(combos[0]?.id || null)
  const [isCreating, setIsCreating] = useState(false)
  const [newComboName, setNewComboName] = useState('')
  const [newComboStrategy, setNewComboStrategy] = useState('fallback')

  // Edit State
  const selectedCombo = combos.find((c) => c.id === selectedComboId)
  const [editingModels, setEditingModels] = useState<string[]>([])
  const [editingStrategy, setEditingStrategy] = useState<string>('fallback')
  const [modelInput, setModelInput] = useState('')
  const [isSaving, setIsSaving] = useState(false)

  // Live Test Playground State
  const [testPrompt, setTestPrompt] = useState('Say hello in 3 words')
  const [testOutput, setTestOutput] = useState('')
  const [isTesting, setIsTesting] = useState(false)

  // Sync editing models when selected combo changes
  React.useEffect(() => {
    if (selectedCombo) {
      try {
        const parsed = JSON.parse(selectedCombo.models)
        setEditingModels(Array.isArray(parsed) ? parsed : [selectedCombo.models])
      } catch {
        setEditingModels(selectedCombo.models ? [selectedCombo.models] : [])
      }
      setEditingStrategy(selectedCombo.strategy || 'fallback')
      setTestOutput('')
    }
  }, [selectedComboId, combos])

  const handleMoveModel = (index: number, delta: number) => {
    const newIdx = index + delta
    if (newIdx < 0 || newIdx >= editingModels.length) return
    const updated = [...editingModels]
    const temp = updated[index]
    updated[index] = updated[newIdx]
    updated[newIdx] = temp
    setEditingModels(updated)
  }

  const handleRemoveModel = (index: number) => {
    setEditingModels(editingModels.filter((_, i) => i !== index))
  }

  const handleAddModel = () => {
    if (!modelInput.trim()) return
    setEditingModels([...editingModels, modelInput.trim()])
    setModelInput('')
  }

  const handleSaveCombo = async () => {
    if (!selectedCombo) return
    try {
      setIsSaving(true)
      await api.updateCombo(selectedCombo.id, {
        models: JSON.stringify(editingModels),
        strategy: editingStrategy,
      })
      onRefresh()
      alert('Combo saved successfully!')
    } catch (err) {
      alert(`Failed to save combo: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setIsSaving(false)
    }
  }

  const handleCreateCombo = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!newComboName.trim()) return
    try {
      setIsSaving(true)
      await api.createCombo({
        name: newComboName.trim(),
        models: JSON.stringify([]),
        strategy: newComboStrategy,
      })
      setIsCreating(false)
      setNewComboName('')
      onRefresh()
    } catch (err) {
      alert(`Failed to create combo: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setIsSaving(false)
    }
  }

  const handleDeleteCombo = async (id: string) => {
    if (!confirm('Are you sure you want to delete this combo?')) return
    try {
      await api.deleteCombo(id)
      onRefresh()
      setSelectedComboId(combos.find((c) => c.id !== id)?.id || null)
    } catch (err) {
      alert(`Failed to delete combo: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  // Live test runner via streaming SSE
  const handleRunLiveTest = async () => {
    if (!selectedCombo) return
    setIsTesting(true)
    setTestOutput('')

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
                setTestOutput((prev) => prev + `[thinking: ${reasoning}]`)
              }
              if (delta) {
                setTestOutput((prev) => prev + delta)
              }
            } catch {
              // ignore malformed chunks
            }
          }
        }
      }
    } catch (err) {
      setTestOutput(`Error: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setIsTesting(false)
    }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold text-white tracking-tight flex items-center gap-2">
            <span>Combos & Smart Routing</span>
            <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 border border-slate-700">
              {combos.length} combos
            </span>
          </h2>
          <p className="text-xs text-slate-400">
            Combine multiple models into unified aliases with automated failover and round-robin
          </p>
        </div>

        <button
          onClick={() => setIsCreating(true)}
          className="flex items-center gap-2 px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold transition shadow-lg shadow-indigo-600/20"
        >
          <Plus className="w-4 h-4" />
          <span>New Combo</span>
        </button>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Left: Combo List */}
        <div className="space-y-2">
          {combos.map((c) => {
            const isSelected = c.id === selectedComboId
            let count = 0
            try {
              count = JSON.parse(c.models).length
            } catch {
              count = c.models ? 1 : 0
            }

            return (
              <div
                key={c.id}
                onClick={() => setSelectedComboId(c.id)}
                className={`p-4 rounded-2xl border cursor-pointer transition-all flex items-center justify-between ${
                  isSelected
                    ? 'bg-slate-900 border-indigo-500/50 shadow-md shadow-indigo-500/10'
                    : 'bg-slate-900/50 border-slate-800/80 hover:border-slate-700'
                }`}
              >
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 rounded-xl bg-indigo-500/10 text-indigo-400 border border-indigo-500/20 flex items-center justify-center font-bold text-xs">
                    <Layers className="w-4 h-4" />
                  </div>
                  <div>
                    <h4 className="text-xs font-bold text-white">{c.name}</h4>
                    <p className="text-[11px] text-slate-400">{count} fallback models</p>
                  </div>
                </div>

                <div className="flex items-center gap-2">
                  <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-950 text-slate-400 border border-slate-800 capitalize">
                    {c.strategy || 'fallback'}
                  </span>
                </div>
              </div>
            )
          })}
        </div>

        {/* Right: Combo Editor & Playground */}
        <div className="lg:col-span-2 space-y-6">
          {selectedCombo ? (
            <div className="bg-slate-900/70 border border-slate-800 rounded-3xl p-6 space-y-6">
              <div className="flex items-center justify-between border-b border-slate-800 pb-4">
                <div>
                  <div className="flex items-center gap-2">
                    <h3 className="text-base font-bold text-white">{selectedCombo.name}</h3>
                    <span className="text-[10px] px-2 py-0.5 rounded-full bg-indigo-500/10 text-indigo-400 border border-indigo-500/20 font-mono">
                      model: "{selectedCombo.name}"
                    </span>
                  </div>
                  <p className="text-xs text-slate-400">Ordered fallback pipeline executed by 9router-go</p>
                </div>

                <div className="flex items-center gap-2">
                  <button
                    onClick={() => handleDeleteCombo(selectedCombo.id)}
                    className="p-2 rounded-xl text-slate-400 hover:text-rose-400 hover:bg-rose-500/10 transition"
                    title="Delete Combo"
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                  <button
                    onClick={handleSaveCombo}
                    disabled={isSaving}
                    className="flex items-center gap-2 px-4 py-2 rounded-xl bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-bold transition shadow-lg shadow-emerald-600/20"
                  >
                    {isSaving ? <Loader2 className="w-4 h-4 animate-spin" /> : <Save className="w-4 h-4" />}
                    <span>Save Combo</span>
                  </button>
                </div>
              </div>

              {/* Strategy selector */}
              <div>
                <label className="block text-xs font-semibold text-slate-300 mb-1.5">Routing Strategy</label>
                <div className="grid grid-cols-2 gap-3 max-w-sm">
                  <button
                    onClick={() => setEditingStrategy('fallback')}
                    className={`p-2.5 rounded-xl border text-xs font-bold transition ${
                      editingStrategy === 'fallback'
                        ? 'bg-indigo-600 border-indigo-500 text-white'
                        : 'bg-slate-950 border-slate-800 text-slate-400'
                    }`}
                  >
                    Sequential Fallback
                  </button>
                  <button
                    onClick={() => setEditingStrategy('round-robin')}
                    className={`p-2.5 rounded-xl border text-xs font-bold transition ${
                      editingStrategy === 'round-robin'
                        ? 'bg-indigo-600 border-indigo-500 text-white'
                        : 'bg-slate-950 border-slate-800 text-slate-400'
                    }`}
                  >
                    Round Robin
                  </button>
                </div>
              </div>

              {/* Fallback models sequence */}
              <div className="space-y-3">
                <label className="block text-xs font-semibold text-slate-300">
                  Model Priority Pipeline (Executed top-to-bottom)
                </label>

                <div className="space-y-2">
                  {editingModels.map((model, idx) => (
                    <div
                      key={idx}
                      className="flex items-center justify-between p-3 rounded-xl bg-slate-950/80 border border-slate-800"
                    >
                      <div className="flex items-center gap-3">
                        <span className="w-6 h-6 rounded-lg bg-slate-800 text-slate-300 flex items-center justify-center font-bold text-xs font-mono">
                          {idx + 1}
                        </span>
                        <span className="text-xs text-slate-200 font-mono font-medium">{model}</span>
                      </div>

                      <div className="flex items-center gap-1">
                        <button
                          onClick={() => handleMoveModel(idx, -1)}
                          disabled={idx === 0}
                          className="p-1 rounded text-slate-400 hover:text-white disabled:opacity-30"
                        >
                          <ArrowUp className="w-3.5 h-3.5" />
                        </button>
                        <button
                          onClick={() => handleMoveModel(idx, 1)}
                          disabled={idx === editingModels.length - 1}
                          className="p-1 rounded text-slate-400 hover:text-white disabled:opacity-30"
                        >
                          <ArrowDown className="w-3.5 h-3.5" />
                        </button>
                        <button
                          onClick={() => handleRemoveModel(idx)}
                          className="p-1 rounded text-slate-400 hover:text-rose-400 ml-2"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    </div>
                  ))}
                </div>

                {/* Add model row */}
                <div className="flex gap-2 pt-2">
                  <input
                    type="text"
                    placeholder="Enter model string (e.g. fb/z-ai/glm-5.3-flash, ag/gemini-3.8-flash-high)"
                    value={modelInput}
                    onChange={(e) => setModelInput(e.target.value)}
                    onKeyDown={(e) => e.key === 'Enter' && handleAddModel()}
                    className="flex-1 px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                  />
                  <button
                    onClick={handleAddModel}
                    className="px-4 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-white text-xs font-bold flex items-center gap-1.5 transition"
                  >
                    <Plus className="w-3.5 h-3.5" />
                    <span>Add</span>
                  </button>
                </div>
              </div>

              {/* Live Test Playground */}
              <div className="pt-4 border-t border-slate-800 space-y-3">
                <h4 className="text-xs font-bold text-white flex items-center gap-2">
                  <Play className="w-3.5 h-3.5 text-emerald-400" />
                  <span>Live Test Playground</span>
                </h4>

                <div className="flex gap-2">
                  <input
                    type="text"
                    value={testPrompt}
                    onChange={(e) => setTestPrompt(e.target.value)}
                    placeholder="Test prompt..."
                    className="flex-1 px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
                  />
                  <button
                    onClick={handleRunLiveTest}
                    disabled={isTesting}
                    className="px-4 py-2 rounded-xl bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-bold flex items-center gap-1.5 transition"
                  >
                    {isTesting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Play className="w-3.5 h-3.5" />}
                    <span>Test Run</span>
                  </button>
                </div>

                {testOutput && (
                  <div className="p-3.5 rounded-xl bg-slate-950 border border-slate-800 text-xs font-mono text-emerald-400 whitespace-pre-wrap max-h-48 overflow-y-auto">
                    {testOutput}
                  </div>
                )}
              </div>
            </div>
          ) : (
            <div className="p-12 text-center text-slate-500 text-xs border border-dashed border-slate-800 rounded-3xl">
              Select a combo to view details and edit pipeline
            </div>
          )}
        </div>
      </div>

      {/* New Combo Modal */}
      {isCreating && (
        <div className="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-center justify-center p-4">
          <div className="bg-slate-900 border border-slate-800 rounded-3xl w-full max-w-md p-6 shadow-2xl space-y-4">
            <h3 className="text-base font-bold text-white flex items-center gap-2">
              <Plus className="w-4 h-4 text-indigo-400" />
              <span>Create New Combo</span>
            </h3>

            <form onSubmit={handleCreateCombo} className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-slate-300 mb-1">Combo Name *</label>
                <input
                  type="text"
                  placeholder="e.g. my-coding-combo"
                  value={newComboName}
                  onChange={(e) => setNewComboName(e.target.value)}
                  required
                  className="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                />
              </div>

              <div>
                <label className="block text-xs font-semibold text-slate-300 mb-1">Routing Strategy</label>
                <select
                  value={newComboStrategy}
                  onChange={(e) => setNewComboStrategy(e.target.value)}
                  className="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
                >
                  <option value="fallback">Sequential Fallback</option>
                  <option value="round-robin">Round Robin</option>
                </select>
              </div>

              <div className="flex justify-end gap-2 pt-2">
                <button
                  type="button"
                  onClick={() => setIsCreating(false)}
                  className="px-4 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-300 text-xs font-bold"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  disabled={isSaving}
                  className="px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold flex items-center gap-1.5"
                >
                  <Check className="w-3.5 h-3.5" />
                  <span>Create Combo</span>
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
