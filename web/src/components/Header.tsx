import React from 'react'
import { Activity, Cpu, Key, Layers, Network, Settings as SettingsIcon } from 'lucide-react'

export type ActiveTab = 'connections' | 'combos' | 'analytics' | 'keys' | 'settings'

interface HeaderProps {
  activeTab: ActiveTab
  setActiveTab: (tab: ActiveTab) => void
  totalConnections: number
  activeConnections: number
  version?: string
}

export const Header: React.FC<HeaderProps> = ({
  activeTab,
  setActiveTab,
  totalConnections,
  activeConnections,
  version = 'v1.8.11',
}) => {
  const tabs = [
    { id: 'connections', label: 'Connections', icon: Network, badge: `${activeConnections}/${totalConnections}` },
    { id: 'combos', label: 'Combos & Routing', icon: Layers },
    { id: 'analytics', label: 'Usage & Stream', icon: Activity },
    { id: 'keys', label: 'API Keys', icon: Key },
    { id: 'settings', label: 'Settings', icon: SettingsIcon },
  ] as const

  return (
    <header className="border-b border-slate-800 bg-[#0d1322]/90 backdrop-blur sticky top-0 z-40 px-6 py-3.5 flex flex-wrap items-center justify-between gap-4">
      <div className="flex items-center gap-3.5">
        <div className="w-9 h-9 rounded-xl bg-gradient-to-br from-indigo-500 to-emerald-500 flex items-center justify-center shadow-lg shadow-indigo-500/20">
          <Cpu className="w-5 h-5 text-white" />
        </div>
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-base font-bold tracking-tight text-white">9Router</h1>
            <span className="text-[10px] font-semibold uppercase tracking-wider px-2 py-0.5 rounded-full bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
              Native Go {version}
            </span>
          </div>
          <p className="text-xs text-slate-400 font-medium">Ultra-low latency AI Gateway & Smart Router</p>
        </div>
      </div>

      <nav className="flex items-center gap-1.5 bg-slate-900/80 p-1 rounded-xl border border-slate-800">
        {tabs.map((tab) => {
          const Icon = tab.icon
          const isActive = activeTab === tab.id
          return (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`flex items-center gap-2 px-3.5 py-1.5 rounded-lg text-xs font-semibold transition-all ${
                isActive
                  ? 'bg-gradient-to-r from-indigo-600 to-indigo-500 text-white shadow-md shadow-indigo-600/30'
                  : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800/60'
              }`}
            >
              <Icon className="w-3.5 h-3.5" />
              <span>{tab.label}</span>
              {'badge' in tab && (
                <span
                  className={`text-[10px] px-1.5 py-0.2 rounded-full font-bold ${
                    isActive ? 'bg-indigo-700 text-white' : 'bg-slate-800 text-slate-400'
                  }`}
                >
                  {tab.badge}
                </span>
              )}
            </button>
          )
        })}
      </nav>
    </header>
  )
}
