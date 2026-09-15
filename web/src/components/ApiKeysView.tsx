import React, { useState } from 'react'
import { Check, Copy, Key, Loader2, Plus, Power, Trash2 } from 'lucide-react'
import { api, type APIKey } from '../api/client'

interface ApiKeysViewProps {
  apiKeys: APIKey[]
  onRefresh: () => void
}

export const ApiKeysView: React.FC<ApiKeysViewProps> = ({ apiKeys, onRefresh }) => {
  const [isCreateOpen, setIsCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const [isCreating, setIsCreating] = useState(false)

  const handleCopy = (text: string, id: string) => {
    navigator.clipboard.writeText(text)
    setCopiedKey(id)
    setTimeout(() => setCopiedKey(null), 2000)
  }

  const handleToggle = async (key: APIKey) => {
    try {
      await api.toggleApiKey(key.id)
      onRefresh()
    } catch (err) {
      alert(`Failed to toggle key: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  const handleDelete = async (id: string) => {
    if (!confirm('Are you sure you want to revoke this API key?')) return
    try {
      await api.deleteApiKey(id)
      onRefresh()
    } catch (err) {
      alert(`Failed to delete key: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault()
    try {
      setIsCreating(true)
      await api.createApiKey({ name: name || 'client-key' })
      setIsCreateOpen(false)
      setName('')
      onRefresh()
    } catch (err) {
      alert(`Failed to create key: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setIsCreating(false)
    }
  }

  const primaryKey = apiKeys[0]?.key || 'sk-your-token-here'

  return (
    <div className="p-6 max-w-7xl mx-auto space-y-6">
      {/* Header */}
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold text-white tracking-tight flex items-center gap-2">
            <span>Client API Keys</span>
            <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 border border-slate-700">
              {apiKeys.length} keys
            </span>
          </h2>
          <p className="text-xs text-slate-400">
            Authorization Bearer tokens for connecting clients (Cursor, Claude Code, omp, Cline)
          </p>
        </div>

        <button
          onClick={() => setIsCreateOpen(true)}
          className="flex items-center gap-2 px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold transition shadow-lg shadow-indigo-600/20"
        >
          <Plus className="w-4 h-4" />
          <span>Generate API Key</span>
        </button>
      </div>

      {/* Keys List */}
      <div className="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <thead>
              <tr className="border-b border-slate-800 text-slate-400 font-medium">
                <th className="py-3 px-4">Name</th>
                <th className="py-3 px-4">Key</th>
                <th className="py-3 px-4">Status</th>
                <th className="py-3 px-4">Created</th>
                <th className="py-3 px-4 text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800/50 font-mono">
              {apiKeys.map((k) => {
                const isActive = k.isActive === 1
                return (
                  <tr key={k.id} className="hover:bg-slate-800/20 transition">
                    <td className="py-3 px-4 font-sans font-bold text-white">{k.name || 'Unnamed Key'}</td>
                    <td className="py-3 px-4 text-slate-300">
                      <div className="flex items-center gap-2">
                        <span className="bg-slate-950 px-2.5 py-1 rounded-lg border border-slate-800 text-[11px]">
                          {k.key}
                        </span>
                        <button
                          onClick={() => handleCopy(k.key, k.id)}
                          className="p-1 rounded text-slate-400 hover:text-white"
                          title="Copy Key"
                        >
                          {copiedKey === k.id ? (
                            <Check className="w-3.5 h-3.5 text-emerald-400" />
                          ) : (
                            <Copy className="w-3.5 h-3.5" />
                          )}
                        </button>
                      </div>
                    </td>
                    <td className="py-3 px-4">
                      <span
                        className={`px-2 py-0.5 rounded text-[10px] font-bold ${
                          isActive
                            ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                            : 'bg-slate-800 text-slate-500'
                        }`}
                      >
                        {isActive ? 'ACTIVE' : 'REVOKED'}
                      </span>
                    </td>
                    <td className="py-3 px-4 text-slate-400 font-sans">
                      {k.createdAt ? new Date(k.createdAt).toLocaleDateString() : '—'}
                    </td>
                    <td className="py-3 px-4 text-right">
                      <div className="flex items-center justify-end gap-1">
                        <button
                          onClick={() => handleToggle(k)}
                          className={`p-1.5 rounded-lg border transition ${
                            isActive
                              ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400'
                              : 'bg-slate-800 border-slate-700 text-slate-500'
                          }`}
                          title={isActive ? 'Deactivate' : 'Activate'}
                        >
                          <Power className="w-3.5 h-3.5" />
                        </button>
                        <button
                          onClick={() => handleDelete(k.id)}
                          className="p-1.5 rounded-lg text-slate-500 hover:text-rose-400 hover:bg-rose-500/10 transition"
                          title="Delete"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>

      {/* Ready-to-use Configuration Snippets */}
      <div className="space-y-3 pt-4">
        <h3 className="text-sm font-bold text-white flex items-center gap-2">
          <Key className="w-4 h-4 text-indigo-400" />
          <span>Quick Client Configuration Snippets</span>
        </h3>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {/* Cursor */}
          <div className="p-4 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-xs font-bold text-white">Cursor IDE</span>
              <span className="text-[10px] text-slate-400">Settings &gt; Models &gt; OpenAI API Key</span>
            </div>
            <div className="p-3 rounded-xl bg-slate-950 border border-slate-800/80 font-mono text-[11px] text-slate-300 space-y-1">
              <div>
                <span className="text-slate-500">Base URL: </span>
                <span className="text-indigo-300">http://localhost:20130/v1</span>
              </div>
              <div>
                <span className="text-slate-500">API Key: </span>
                <span className="text-emerald-300 truncate">{primaryKey}</span>
              </div>
            </div>
          </div>

          {/* Claude Code */}
          <div className="p-4 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
            <div className="flex items-center justify-between">
              <span className="text-xs font-bold text-white">Claude Code CLI</span>
              <span className="text-[10px] text-slate-400">Terminal Environment</span>
            </div>
            <div className="p-3 rounded-xl bg-slate-950 border border-slate-800/80 font-mono text-[11px] text-slate-300 space-y-1">
              <div>export ANTHROPIC_BASE_URL="http://localhost:20130"</div>
              <div>export ANTHROPIC_API_KEY="{primaryKey}"</div>
            </div>
          </div>
        </div>
      </div>

      {/* Create Modal */}
      {isCreateOpen && (
        <div className="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-center justify-center p-4">
          <div className="bg-slate-900 border border-slate-800 rounded-3xl w-full max-w-md p-6 shadow-2xl space-y-4">
            <h3 className="text-base font-bold text-white flex items-center gap-2">
              <Plus className="w-4 h-4 text-indigo-400" />
              <span>Create New Client Key</span>
            </h3>

            <form onSubmit={handleCreate} className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-slate-300 mb-1">Key Label</label>
                <input
                  type="text"
                  placeholder="e.g. cursor-laptop, omp-workstation"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  className="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
                />
              </div>

              <div className="flex justify-end gap-2 pt-2">
                <button
                  type="button"
                  onClick={() => setIsCreateOpen(false)}
                  className="px-4 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-300 text-xs font-bold"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  disabled={isCreating}
                  className="px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold flex items-center gap-1.5"
                >
                  {isCreating ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Check className="w-3.5 h-3.5" />}
                  <span>Generate Key</span>
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
