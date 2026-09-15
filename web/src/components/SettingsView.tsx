import React, { useState } from 'react'
import { Check, Loader2, RefreshCw, Save, Shield, Zap } from 'lucide-react'
import { api, type Settings } from '../api/client'

interface SettingsViewProps {
  settings: Settings
  onRefresh: () => void
}

export const SettingsView: React.FC<SettingsViewProps> = ({ settings, onRefresh }) => {
  const [formData, setFormData] = useState<Settings>({ ...settings })
  const [isSaving, setIsSaving] = useState(false)
  const [resetProvider, setResetProvider] = useState('antigravity')
  const [isResetting, setIsResetting] = useState(false)

  React.useEffect(() => {
    setFormData({ ...settings })
  }, [settings])

  const handleSave = async () => {
    try {
      setIsSaving(true)
      await api.updateSettings(formData)
      onRefresh()
      alert('Settings updated successfully!')
    } catch (err) {
      alert(`Failed to save settings: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setIsSaving(false)
    }
  }

  const handleResetHealth = async () => {
    try {
      setIsResetting(true)
      await api.resetHealth(resetProvider)
      alert(`Health state and rate-limit locks for '${resetProvider}' have been reset.`)
    } catch (err) {
      alert(`Failed to reset health: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setIsResetting(false)
    }
  }

  return (
    <div className="p-6 max-w-4xl mx-auto space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-xl font-bold text-white tracking-tight">System & Proxy Settings</h2>
          <p className="text-xs text-slate-400">Configure gateway security, token saver engines, and failover health</p>
        </div>

        <button
          onClick={handleSave}
          disabled={isSaving}
          className="flex items-center gap-2 px-4 py-2 rounded-xl bg-indigo-600 hover:bg-indigo-500 text-white text-xs font-bold transition shadow-lg shadow-indigo-600/20"
        >
          {isSaving ? <Loader2 className="w-4 h-4 animate-spin" /> : <Save className="w-4 h-4" />}
          <span>Save Changes</span>
        </button>
      </div>

      {/* Security Section */}
      <div className="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
        <h3 className="text-sm font-bold text-white flex items-center gap-2">
          <Shield className="w-4 h-4 text-indigo-400" />
          <span>Security & Access Control</span>
        </h3>

        <div className="flex items-center justify-between p-4 rounded-2xl bg-slate-950/80 border border-slate-800">
          <div>
            <div className="text-xs font-bold text-white">Require Client API Key</div>
            <div className="text-[11px] text-slate-400">
              When enabled, incoming requests must supply a valid Bearer token from the API Keys table
            </div>
          </div>

          <label className="relative inline-flex items-center cursor-pointer">
            <input
              type="checkbox"
              checked={!!formData.requireApiKey}
              onChange={(e) => setFormData({ ...formData, requireApiKey: e.target.checked })}
              className="sr-only peer"
            />
            <div className="w-11 h-6 bg-slate-800 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-indigo-600"></div>
          </label>
        </div>
      </div>

      {/* Token Savers Section */}
      <div className="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
        <h3 className="text-sm font-bold text-white flex items-center gap-2">
          <Zap className="w-4 h-4 text-emerald-400" />
          <span>Token Saver Engines</span>
        </h3>

        <div className="space-y-3">
          {/* RTK */}
          <div className="flex items-center justify-between p-4 rounded-2xl bg-slate-950/80 border border-slate-800">
            <div>
              <div className="text-xs font-bold text-white flex items-center gap-2">
                <span>RTK Compression</span>
                <span className="text-[9px] px-1.5 py-0.2 rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 font-mono">
                  Recommended
                </span>
              </div>
              <div className="text-[11px] text-slate-400">
                Filters repetitive CLI, build, and git output to reduce prompt tokens by 60-80% without losing quality
              </div>
            </div>

            <label className="relative inline-flex items-center cursor-pointer">
              <input
                type="checkbox"
                checked={!!formData.rtkEnabled}
                onChange={(e) => setFormData({ ...formData, rtkEnabled: e.target.checked })}
                className="sr-only peer"
              />
              <div className="w-11 h-6 bg-slate-800 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-emerald-600"></div>
            </label>
          </div>

          {/* Caveman */}
          <div className="flex items-center justify-between p-4 rounded-2xl bg-slate-950/80 border border-slate-800">
            <div>
              <div className="text-xs font-bold text-white">Caveman Terse Output</div>
              <div className="text-[11px] text-slate-400">Instructs model to reply with ultra-succinct, non-hedging language</div>
            </div>

            <label className="relative inline-flex items-center cursor-pointer">
              <input
                type="checkbox"
                checked={!!formData.cavemanEnabled}
                onChange={(e) => setFormData({ ...formData, cavemanEnabled: e.target.checked })}
                className="sr-only peer"
              />
              <div className="w-11 h-6 bg-slate-800 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-indigo-600"></div>
            </label>
          </div>

          {/* Ponytail */}
          <div className="flex items-center justify-between p-4 rounded-2xl bg-slate-950/80 border border-slate-800">
            <div>
              <div className="text-xs font-bold text-white">Ponytail Code Style</div>
              <div className="text-[11px] text-slate-400">Enforces pragmatic, minimal boilerplate coding conventions</div>
            </div>

            <label className="relative inline-flex items-center cursor-pointer">
              <input
                type="checkbox"
                checked={!!formData.ponytailEnabled}
                onChange={(e) => setFormData({ ...formData, ponytailEnabled: e.target.checked })}
                className="sr-only peer"
              />
              <div className="w-11 h-6 bg-slate-800 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-slate-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-indigo-600"></div>
            </label>
          </div>
        </div>
      </div>

      {/* Failover Health Cache Reset */}
      <div className="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
        <h3 className="text-sm font-bold text-white flex items-center gap-2">
          <RefreshCw className="w-4 h-4 text-indigo-400" />
          <span>Manual Health & Rate Limit Cache Reset</span>
        </h3>
        <p className="text-xs text-slate-400">
          If an account encountered HTTP 429 and was locked in cooldown, you can manually clear its lock here
        </p>

        <div className="flex items-center gap-3">
          <select
            value={resetProvider}
            onChange={(e) => setResetProvider(e.target.value)}
            className="px-3 py-2 rounded-xl bg-slate-950 border border-slate-800 text-xs text-slate-200 focus:outline-none focus:border-indigo-500"
          >
            <option value="antigravity">antigravity</option>
            <option value="freebuff">freebuff</option>
            <option value="clinepass">clinepass</option>
            <option value="deepseek">deepseek</option>
            <option value="groq">groq</option>
          </select>

          <button
            onClick={handleResetHealth}
            disabled={isResetting}
            className="px-4 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-white text-xs font-bold flex items-center gap-2 transition"
          >
            {isResetting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Check className="w-3.5 h-3.5" />}
            <span>Clear Rate Limit Lock</span>
          </button>
        </div>
      </div>
    </div>
  )
}
