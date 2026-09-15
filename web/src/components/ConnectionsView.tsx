import React, { useState } from 'react'
import {
  AlertCircle,
  ArrowDown,
  ArrowUp,
  Check,
  ExternalLink,
  Key,
  Loader2,
  Plus,
  Power,
  RefreshCw,
  Search,
  Trash2,
  Zap,
} from 'lucide-react'
import { api, type ProviderConnection } from '../api/client'

interface ConnectionsViewProps {
  connections: ProviderConnection[]
  onRefresh: () => void
}

export const ConnectionsView: React.FC<ConnectionsViewProps> = ({ connections, onRefresh }) => {
  const [search, setSearch] = useState('')
  const [filterType, setFilterType] = useState<'all' | 'oauth' | 'apikey'>('all')
  const [isAddModalOpen, setIsAddModalOpen] = useState(false)
  const [updatingId, setUpdatingId] = useState<string | null>(null)

  // Add Connection state
  const [addMode, setAddMode] = useState<'oauth' | 'apikey'>('oauth')
  const [oauthProvider, setOauthProvider] = useState<'freebuff' | 'antigravity'>('freebuff')
  const [fbFlowState, setFbFlowState] = useState<{
    status: 'idle' | 'initiating' | 'polling' | 'authorized' | 'error'
    loginUrl?: string
    authCode?: string
    error?: string
  }>({ status: 'idle' })

  // API Key Form State
  const [apiKeyProvider, setApiKeyProvider] = useState('deepseek')
  const [apiKeyName, setApiKeyName] = useState('')
  const [apiKeyValue, setApiKeyValue] = useState('')
  const [apiBaseUrl, setApiBaseUrl] = useState('')
  const [isSavingKey, setIsSavingKey] = useState(false)

  const filteredConnections = connections.filter((c) => {
    const matchesSearch =
      c.provider.toLowerCase().includes(search.toLowerCase()) ||
      (c.name && c.name.toLowerCase().includes(search.toLowerCase())) ||
      (c.email && c.email.toLowerCase().includes(search.toLowerCase())) ||
      c.id.toLowerCase().includes(search.toLowerCase())
    if (!matchesSearch) return false
    if (filterType === 'oauth') return c.authType === 'oauth'
    if (filterType === 'apikey') return c.authType !== 'oauth'
    return true
  })

  const handleToggleActive = async (conn: ProviderConnection) => {
    try {
      setUpdatingId(conn.id)
      const nextActive = conn.isActive === 1 ? 0 : 1
      await api.updateConnection(conn.id, { isActive: nextActive })
      onRefresh()
    } catch (err) {
      alert(`Failed to toggle status: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setUpdatingId(null)
    }
  }

  const handlePriorityChange = async (conn: ProviderConnection, delta: number) => {
    try {
      setUpdatingId(conn.id)
      const currentPriority = conn.priority ?? 999999
      const nextPriority = Math.max(1, currentPriority + delta)
      await api.updateConnection(conn.id, { priority: nextPriority })
      onRefresh()
    } catch (err) {
      alert(`Failed to update priority: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setUpdatingId(null)
    }
  }

  const handleDelete = async (id: string) => {
    if (!confirm('Are you sure you want to delete this provider connection?')) return
    try {
      setUpdatingId(id)
      await api.deleteConnection(id)
      onRefresh()
    } catch (err) {
      alert(`Failed to delete connection: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setUpdatingId(null)
    }
  }

  // Freebuff Device Flow execution
  const startFreebuffFlow = async () => {
    try {
      setFbFlowState({ status: 'initiating' })
      const init = await api.initiateFreebuff()
      setFbFlowState({
        status: 'polling',
        loginUrl: init.loginUrl,
        authCode: init.authCode,
      })

      // Open Freebuff login page in new tab
      window.open(init.loginUrl, '_blank')

      // Poll every 3 seconds for up to 5 minutes
      const interval = setInterval(async () => {
        try {
          const poll = await api.pollFreebuff(init.fingerprintId, init.fingerprintHash)
          if (poll.status === 'authorized') {
            clearInterval(interval)
            setFbFlowState({ status: 'authorized' })
            setTimeout(() => {
              setIsAddModalOpen(false)
              setFbFlowState({ status: 'idle' })
              onRefresh()
            }, 1500)
          } else if (poll.status === 'expired') {
            clearInterval(interval)
            setFbFlowState({ status: 'error', error: 'Login session expired. Please retry.' })
          }
        } catch {
          // keep polling until timeout
        }
      }, 3000)

      setTimeout(() => clearInterval(interval), 300000)
    } catch (err) {
      setFbFlowState({
        status: 'error',
        error: err instanceof Error ? err.message : String(err),
      })
    }
  }

  const startAntigravityFlow = async () => {
    try {
      const auth = await api.getAntigravityAuthorizeUrl()
      window.location.href = auth.url || auth.redirectUrl
    } catch (err) {
      alert(`Failed to start Google OAuth: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  const handleSaveApiKey = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!apiKeyValue) {
      alert('API key is required')
      return
    }
    try {
      setIsSavingKey(true)
      const dataObj: Record<string, string> = { apiKey: apiKeyValue }
      if (apiBaseUrl) dataObj.baseUrl = apiBaseUrl

      await api.createConnection({
        provider: apiKeyProvider,
        authType: 'apikey',
        name: apiKeyName || `${apiKeyProvider}-connection`,
        apiKey: apiKeyValue,
        data: JSON.stringify(dataObj),
      })
      setIsAddModalOpen(false)
      setApiKeyValue('')
      setApiKeyName('')
      setApiBaseUrl('')
      onRefresh()
    } catch (err) {
      alert(`Failed to save connection: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setIsSavingKey(false)
    }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto space-y-6">
      {/* Top Bar */}
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4">
        <div>
          <h2 className="text-xl font-bold text-white tracking-tight flex items-center gap-2">
            <span>Provider Connections</span>
            <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 border border-slate-700">
              {connections.length} total
            </span>
          </h2>
          <p className="text-xs text-slate-400">Manage LLM upstream accounts, priority order, and OAuth integrations</p>
        </div>

        <div className="flex items-center gap-2.5 w-full sm:w-auto">
          <button
            onClick={onRefresh}
            className="p-2 rounded-xl bg-slate-800/80 hover:bg-slate-700 text-slate-300 transition border border-slate-700 hover:text-white"
            title="Refresh list"
          >
            <RefreshCw className="w-4 h-4" />
          </button>
          <button
            onClick={() => setIsAddModalOpen(true)}
            className="flex items-center gap-2 px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold transition shadow-lg shadow-indigo-600/20"
          >
            <Plus className="w-4 h-4" />
            <span>Add Connection</span>
          </button>
        </div>
      </div>

      {/* Filters & Search */}
      <div className="flex flex-col sm:flex-row items-center justify-between gap-3 bg-slate-900/60 p-2.5 rounded-2xl border border-slate-800/80">
        <div className="relative w-full sm:w-80">
          <Search className="w-4 h-4 absolute left-3 top-1/2 -translate-y-1/2 text-slate-500" />
          <input
            type="text"
            placeholder="Search provider, email, ID..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="w-full pl-9 pr-4 py-1.5 rounded-xl bg-slate-950/80 border border-slate-800 text-xs text-slate-200 placeholder-slate-500 focus:outline-none focus:border-indigo-500"
          />
        </div>

        <div className="flex items-center gap-1 self-start sm:self-auto">
          {(['all', 'oauth', 'apikey'] as const).map((type) => (
            <button
              key={type}
              onClick={() => setFilterType(type)}
              className={`px-3 py-1 rounded-lg text-xs font-medium capitalize transition ${
                filterType === type
                  ? 'bg-slate-800 text-white font-semibold border border-slate-700'
                  : 'text-slate-400 hover:text-slate-200'
              }`}
            >
              {type === 'apikey' ? 'API Key' : type}
            </button>
          ))}
        </div>
      </div>

      {/* Connection Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {filteredConnections.map((conn) => {
          const isActive = conn.isActive === 1
          const isBusy = updatingId === conn.id

          return (
            <div
              key={conn.id}
              className={`rounded-2xl border p-4 transition-all duration-200 flex flex-col justify-between ${
                isActive
                  ? 'bg-slate-900/60 border-slate-800 hover:border-slate-700/80 shadow-sm'
                  : 'bg-slate-950/40 border-slate-900 opacity-60'
              }`}
            >
              <div>
                <div className="flex items-start justify-between gap-2 mb-3">
                  <div className="flex items-center gap-2.5">
                    <div
                      className={`w-8 h-8 rounded-xl flex items-center justify-center font-bold text-xs ${
                        conn.provider.includes('antigravity') || conn.provider.includes('gemini')
                          ? 'bg-blue-500/10 text-blue-400 border border-blue-500/20'
                          : conn.provider.includes('freebuff')
                          ? 'bg-lime-500/10 text-lime-400 border border-lime-500/20'
                          : conn.provider.includes('cline')
                          ? 'bg-amber-500/10 text-amber-400 border border-amber-500/20'
                          : 'bg-indigo-500/10 text-indigo-400 border border-indigo-500/20'
                      }`}
                    >
                      {conn.provider.slice(0, 2).toUpperCase()}
                    </div>
                    <div>
                      <div className="flex items-center gap-1.5">
                        <span className="text-xs font-bold text-white capitalize">{conn.provider}</span>
                        <span
                          className={`text-[9px] px-1.5 py-0.2 rounded font-semibold uppercase ${
                            conn.authType === 'oauth'
                              ? 'bg-purple-500/10 text-purple-400 border border-purple-500/20'
                              : 'bg-slate-800 text-slate-400'
                          }`}
                        >
                          {conn.authType}
                        </span>
                      </div>
                      <p className="text-[11px] text-slate-400 truncate max-w-[170px]">
                        {conn.name || conn.email || conn.id}
                      </p>
                    </div>
                  </div>

                  <button
                    onClick={() => handleToggleActive(conn)}
                    disabled={isBusy}
                    className={`p-1.5 rounded-lg border transition ${
                      isActive
                        ? 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400 hover:bg-emerald-500/20'
                        : 'bg-slate-800 border-slate-700 text-slate-500 hover:text-slate-300'
                    }`}
                    title={isActive ? 'Deactivate connection' : 'Activate connection'}
                  >
                    {isBusy ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Power className="w-3.5 h-3.5" />}
                  </button>
                </div>

                <div className="space-y-1 text-[11px] text-slate-400 font-mono bg-slate-950/60 p-2 rounded-xl border border-slate-800/60 mb-3">
                  <div className="flex justify-between">
                    <span className="text-slate-500">Priority:</span>
                    <span className="text-slate-300 font-semibold">{conn.priority ?? 'None (999999)'}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-slate-500">ID:</span>
                    <span className="text-slate-300 truncate max-w-[130px]" title={conn.id}>
                      {conn.id}
                    </span>
                  </div>
                </div>
              </div>

              <div className="flex items-center justify-between pt-2 border-t border-slate-800/60">
                <div className="flex items-center gap-1">
                  <button
                    onClick={() => handlePriorityChange(conn, -1)}
                    className="p-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white transition"
                    title="Increase Priority (lower number)"
                  >
                    <ArrowUp className="w-3 h-3" />
                  </button>
                  <button
                    onClick={() => handlePriorityChange(conn, 1)}
                    className="p-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white transition"
                    title="Decrease Priority (higher number)"
                  >
                    <ArrowDown className="w-3 h-3" />
                  </button>
                </div>

                <button
                  onClick={() => handleDelete(conn.id)}
                  className="p-1 rounded text-slate-500 hover:text-rose-400 hover:bg-rose-500/10 transition"
                  title="Delete Connection"
                >
                  <Trash2 className="w-3.5 h-3.5" />
                </button>
              </div>
            </div>
          )
        })}
      </div>

      {/* Add Connection Modal */}
      {isAddModalOpen && (
        <div className="fixed inset-0 z-50 bg-black/70 backdrop-blur-sm flex items-center justify-center p-4">
          <div className="bg-slate-900 border border-slate-800 rounded-3xl w-full max-w-lg p-6 shadow-2xl relative">
            <div className="flex items-center justify-between mb-5">
              <h3 className="text-base font-bold text-white flex items-center gap-2">
                <Plus className="w-4 h-4 text-indigo-400" />
                <span>Add Provider Connection</span>
              </h3>
              <button
                onClick={() => {
                  setIsAddModalOpen(false)
                  setFbFlowState({ status: 'idle' })
                }}
                className="text-slate-400 hover:text-white text-xs px-2 py-1 rounded-lg bg-slate-800"
              >
                ✕ Close
              </button>
            </div>

            {/* Mode Switcher */}
            <div className="flex rounded-xl bg-slate-950 p-1 border border-slate-800 mb-5">
              <button
                onClick={() => setAddMode('oauth')}
                className={`flex-1 py-1.5 rounded-lg text-xs font-bold transition ${
                  addMode === 'oauth' ? 'bg-indigo-600 text-white' : 'text-slate-400 hover:text-white'
                }`}
              >
                OAuth Device Flows (Freebuff / Google)
              </button>
              <button
                onClick={() => setAddMode('apikey')}
                className={`flex-1 py-1.5 rounded-lg text-xs font-bold transition ${
                  addMode === 'apikey' ? 'bg-indigo-600 text-white' : 'text-slate-400 hover:text-white'
                }`}
              >
                API Key Providers
              </button>
            </div>

            {addMode === 'oauth' ? (
              <div className="space-y-4">
                <div className="grid grid-cols-2 gap-3">
                  <button
                    onClick={() => setOauthProvider('freebuff')}
                    className={`p-3.5 rounded-2xl border text-left transition ${
                      oauthProvider === 'freebuff'
                        ? 'bg-lime-500/10 border-lime-500/30 text-lime-300 shadow-sm'
                        : 'bg-slate-950/60 border-slate-800 text-slate-400 hover:border-slate-700'
                    }`}
                  >
                    <div className="font-bold text-xs mb-1">Freebuff (Codebuff)</div>
                    <div className="text-[10px] text-slate-400">Freebucks daily quota (GLM 5.3, Solar Pro, DeepSeek)</div>
                  </button>
                  <button
                    onClick={() => setOauthProvider('antigravity')}
                    className={`p-3.5 rounded-2xl border text-left transition ${
                      oauthProvider === 'antigravity'
                        ? 'bg-blue-500/10 border-blue-500/30 text-blue-300 shadow-sm'
                        : 'bg-slate-950/60 border-slate-800 text-slate-400 hover:border-slate-700'
                    }`}
                  >
                    <div className="font-bold text-xs mb-1">Google Antigravity</div>
                    <div className="text-[10px] text-slate-400">Multi-account failover for Gemini 2.5 / 3.7</div>
                  </button>
                </div>

                {oauthProvider === 'freebuff' ? (
                  <div className="bg-slate-950/80 p-4 rounded-2xl border border-slate-800/80 space-y-3">
                    <p className="text-xs text-slate-300">
                      Login using official Freebuff Device Flow. Click the button below to generate a login link.
                    </p>

                    {fbFlowState.status === 'idle' && (
                      <button
                        onClick={startFreebuffFlow}
                        className="w-full py-2.5 rounded-xl bg-lime-600 hover:bg-lime-500 text-slate-950 font-bold text-xs flex items-center justify-center gap-2 transition"
                      >
                        <Zap className="w-4 h-4" />
                        <span>Start Freebuff Login Flow</span>
                      </button>
                    )}

                    {fbFlowState.status === 'initiating' && (
                      <div className="flex items-center justify-center py-4 text-xs text-slate-400 gap-2">
                        <Loader2 className="w-4 h-4 animate-spin text-lime-400" />
                        <span>Generating device session...</span>
                      </div>
                    )}

                    {fbFlowState.status === 'polling' && (
                      <div className="space-y-3">
                        <div className="flex items-center gap-2 text-xs text-lime-400 font-semibold">
                          <Loader2 className="w-4 h-4 animate-spin" />
                          <span>Waiting for browser authorization...</span>
                        </div>
                        <div className="bg-slate-900 p-3 rounded-xl border border-slate-800 text-[11px] space-y-1">
                          <div className="text-slate-400">If browser did not open automatically, visit:</div>
                          <a
                            href={fbFlowState.loginUrl}
                            target="_blank"
                            rel="noreferrer"
                            className="text-indigo-400 underline font-mono break-all flex items-center gap-1"
                          >
                            <span>{fbFlowState.loginUrl}</span>
                            <ExternalLink className="w-3 h-3 flex-shrink-0" />
                          </a>
                        </div>
                      </div>
                    )}

                    {fbFlowState.status === 'authorized' && (
                      <div className="flex items-center gap-2 text-emerald-400 text-xs font-bold py-2">
                        <Check className="w-4 h-4" />
                        <span>Account authorized and saved successfully!</span>
                      </div>
                    )}

                    {fbFlowState.status === 'error' && (
                      <div className="flex items-center gap-2 text-rose-400 text-xs font-medium">
                        <AlertCircle className="w-4 h-4" />
                        <span>{fbFlowState.error}</span>
                      </div>
                    )}
                  </div>
                ) : (
                  <div className="bg-slate-950/80 p-4 rounded-2xl border border-slate-800/80 space-y-3">
                    <p className="text-xs text-slate-300">
                      Connect your Google Account to enable Antigravity Gemini high/low capacity models.
                    </p>
                    <button
                      onClick={startAntigravityFlow}
                      className="w-full py-2.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white font-bold text-xs flex items-center justify-center gap-2 transition shadow-lg shadow-blue-600/20"
                    >
                      <ExternalLink className="w-4 h-4" />
                      <span>Sign in with Google OAuth</span>
                    </button>
                  </div>
                )}
              </div>
            ) : (
              <form onSubmit={handleSaveApiKey} className="space-y-3.5">
                <div>
                  <label className="block text-xs font-semibold text-slate-300 mb-1">Provider</label>
                  <select
                    value={apiKeyProvider}
                    onChange={(e) => setApiKeyProvider(e.target.value)}
                    className="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
                  >
                    <option value="deepseek">DeepSeek (deepseek-chat, coder)</option>
                    <option value="groq">Groq (Llama-3, fast inference)</option>
                    <option value="openrouter">OpenRouter</option>
                    <option value="gemini">Google Gemini (Direct API Key)</option>
                    <option value="nvidia">Nvidia NIM</option>
                    <option value="openai-compatible-chat">Custom OpenAI-Compatible Endpoint</option>
                  </select>
                </div>

                <div>
                  <label className="block text-xs font-semibold text-slate-300 mb-1">Connection Name</label>
                  <input
                    type="text"
                    placeholder="e.g. My Primary DeepSeek"
                    value={apiKeyName}
                    onChange={(e) => setApiKeyName(e.target.value)}
                    className="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
                  />
                </div>

                <div>
                  <label className="block text-xs font-semibold text-slate-300 mb-1">API Key *</label>
                  <input
                    type="password"
                    placeholder="sk-..."
                    value={apiKeyValue}
                    onChange={(e) => setApiKeyValue(e.target.value)}
                    required
                    className="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                  />
                </div>

                {apiKeyProvider === 'openai-compatible-chat' && (
                  <div>
                    <label className="block text-xs font-semibold text-slate-300 mb-1">Base URL</label>
                    <input
                      type="url"
                      placeholder="https://api.together.xyz/v1"
                      value={apiBaseUrl}
                      onChange={(e) => setApiBaseUrl(e.target.value)}
                      className="w-full px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 font-mono focus:outline-none focus:border-indigo-500"
                    />
                  </div>
                )}

                <button
                  type="submit"
                  disabled={isSavingKey}
                  className="w-full mt-2 py-2.5 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white font-bold text-xs flex items-center justify-center gap-2 transition shadow-lg shadow-indigo-600/20"
                >
                  {isSavingKey ? <Loader2 className="w-4 h-4 animate-spin" /> : <Key className="w-4 h-4" />}
                  <span>Save Connection</span>
                </button>
              </form>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
