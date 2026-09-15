import React, { useEffect, useState } from 'react'
import { Activity, Clock, Cpu, Radio, Zap } from 'lucide-react'
import { api, getAuthHeaders } from '../api/client'

interface StreamEvent {
  id: string
  timestamp: string
  provider?: string
  model?: string
  promptTokens?: number
  completionTokens?: number
  cost?: number
  status?: string
}

export const AnalyticsView: React.FC = () => {
  const [stats, setStats] = useState<{
    promptTokens?: number
    completionTokens?: number
    totalTokens?: number
    requests?: number
  }>({})
  const [events, setEvents] = useState<StreamEvent[]>([])
  const [isConnected, setIsConnected] = useState(false)

  // Fetch initial usage stats
  useEffect(() => {
    api
      .getUsageStats()
      .then((data) => {
        if (data && typeof data === 'object') {
          setStats(data as typeof stats)
        }
      })
      .catch(() => {})
  }, [])

  // Listen to live SSE usage stream
  useEffect(() => {
    // EventSource doesn't natively support custom headers, but 9router allows token in URL query or cookie if needed
    // or we use standard fetch with reader
    let isCancelled = false

    const connectStream = async () => {
      try {
        const res = await fetch('/usage/stream', {
          headers: getAuthHeaders(),
        })

        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        setIsConnected(true)

        const reader = res.body?.getReader()
        const decoder = new TextDecoder()
        if (!reader) return

        let buffer = ''
        while (!isCancelled) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const lines = buffer.split('\n')
          buffer = lines.pop() || ''

          for (const line of lines) {
            const trimmed = line.trim()
            if (!trimmed || trimmed.startsWith(':')) continue
            if (trimmed.startsWith('data: ')) {
              try {
                const parsed = JSON.parse(trimmed.slice(6))
                setEvents((prev) => [parsed, ...prev.slice(0, 49)]) // keep last 50 events
              } catch {
                // ignore
              }
            }
          }
        }
      } catch {
        setIsConnected(false)
        // retry in 5s
        if (!isCancelled) setTimeout(connectStream, 5000)
      }
    }

    connectStream()

    return () => {
      isCancelled = true
    }
  }, [])

  return (
    <div className="p-6 max-w-7xl mx-auto space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-xl font-bold text-white tracking-tight flex items-center gap-2">
            <span>Live Analytics & Stream</span>
            <span
              className={`flex items-center gap-1 text-[10px] font-bold px-2 py-0.5 rounded-full ${
                isConnected
                  ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                  : 'bg-amber-500/10 text-amber-400 border border-amber-500/20'
              }`}
            >
              <Radio className="w-3 h-3 animate-pulse" />
              <span>{isConnected ? 'LIVE STREAM CONNECTED' : 'CONNECTING...'}</span>
            </span>
          </h2>
          <p className="text-xs text-slate-400">Real-time token metrics, throughput, and upstream SSE event logs</p>
        </div>
      </div>

      {/* Metric Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <div className="p-5 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
          <div className="flex items-center justify-between text-slate-400 text-xs">
            <span>Prompt Tokens</span>
            <Cpu className="w-4 h-4 text-indigo-400" />
          </div>
          <div className="text-2xl font-bold text-white font-mono">
            {stats.promptTokens?.toLocaleString() || '—'}
          </div>
        </div>

        <div className="p-5 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
          <div className="flex items-center justify-between text-slate-400 text-xs">
            <span>Completion Tokens</span>
            <Zap className="w-4 h-4 text-emerald-400" />
          </div>
          <div className="text-2xl font-bold text-white font-mono">
            {stats.completionTokens?.toLocaleString() || '—'}
          </div>
        </div>

        <div className="p-5 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
          <div className="flex items-center justify-between text-slate-400 text-xs">
            <span>Total Tokens</span>
            <Activity className="w-4 h-4 text-purple-400" />
          </div>
          <div className="text-2xl font-bold text-white font-mono">
            {stats.totalTokens?.toLocaleString() || '—'}
          </div>
        </div>

        <div className="p-5 rounded-2xl bg-slate-900/60 border border-slate-800 space-y-2">
          <div className="flex items-center justify-between text-slate-400 text-xs">
            <span>Total Requests</span>
            <Clock className="w-4 h-4 text-amber-400" />
          </div>
          <div className="text-2xl font-bold text-white font-mono">
            {stats.requests?.toLocaleString() || '—'}
          </div>
        </div>
      </div>

      {/* Live Stream Table */}
      <div className="bg-slate-900/60 border border-slate-800 rounded-3xl p-6 space-y-4">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-bold text-white flex items-center gap-2">
            <Radio className="w-4 h-4 text-emerald-400" />
            <span>Live Stream Activity Feed</span>
          </h3>
          <span className="text-xs text-slate-500 font-mono">Auto-scrolling stream events</span>
        </div>

        <div className="overflow-x-auto">
          <table className="w-full text-left text-xs">
            <thead>
              <tr className="border-b border-slate-800 text-slate-400 font-medium">
                <th className="py-2.5 px-3">Time</th>
                <th className="py-2.5 px-3">Provider</th>
                <th className="py-2.5 px-3">Model</th>
                <th className="py-2.5 px-3">Prompt</th>
                <th className="py-2.5 px-3">Completion</th>
                <th className="py-2.5 px-3">Status</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800/50 font-mono">
              {events.length > 0 ? (
                events.map((evt, i) => (
                  <tr key={i} className="hover:bg-slate-800/30 transition">
                    <td className="py-2.5 px-3 text-slate-400">
                      {evt.timestamp ? new Date(evt.timestamp).toLocaleTimeString() : 'Just now'}
                    </td>
                    <td className="py-2.5 px-3 font-semibold text-indigo-400">{evt.provider || '—'}</td>
                    <td className="py-2.5 px-3 text-slate-200">{evt.model || '—'}</td>
                    <td className="py-2.5 px-3 text-slate-400">{evt.promptTokens || 0}</td>
                    <td className="py-2.5 px-3 text-slate-400">{evt.completionTokens || 0}</td>
                    <td className="py-2.5 px-3">
                      <span
                        className={`px-2 py-0.5 rounded text-[10px] font-bold ${
                          evt.status === 'success' || !evt.status
                            ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                            : 'bg-rose-500/10 text-rose-400 border border-rose-500/20'
                        }`}
                      >
                        {evt.status || 'success'}
                      </span>
                    </td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={6} className="py-12 text-center text-slate-500">
                    Listening for incoming streaming requests...
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}
