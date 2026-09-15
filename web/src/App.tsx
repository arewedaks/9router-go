import { useEffect, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { api, type APIKey, type Combo, type ProviderConnection, type Settings } from './api/client'
import { AnalyticsView } from './components/AnalyticsView'
import { ApiKeysView } from './components/ApiKeysView'
import { CombosView } from './components/CombosView'
import { ConnectionsView } from './components/ConnectionsView'
import { type ActiveTab, Header } from './components/Header'
import { SettingsView } from './components/SettingsView'

export function App() {
  const [activeTab, setActiveTab] = useState<ActiveTab>('connections')
  const [connections, setConnections] = useState<ProviderConnection[]>([])
  const [combos, setCombos] = useState<Combo[]>([])
  const [apiKeys, setApiKeys] = useState<APIKey[]>([])
  const [settings, setSettings] = useState<Settings>({})
  const [isLoading, setIsLoading] = useState(true)

  const loadData = async () => {
    try {
      const [connsRes, combosRes, keysRes, settingsRes] = await Promise.all([
        api.getConnections().catch(() => []),
        api.getCombos().catch(() => []),
        api.getApiKeys().catch(() => []),
        api.getSettings().catch(() => ({})),
      ])
      setConnections(connsRes)
      setCombos(combosRes)
      setApiKeys(keysRes)
      setSettings(settingsRes)
    } finally {
      setIsLoading(false)
    }
  }

  useEffect(() => {
    loadData()
  }, [])

  const activeConnectionsCount = connections.filter((c) => c.isActive === 1).length

  return (
    <div className="min-h-screen bg-[#090d16] text-slate-100 flex flex-col font-sans">
      <Header
        activeTab={activeTab}
        setActiveTab={setActiveTab}
        totalConnections={connections.length}
        activeConnections={activeConnectionsCount}
      />

      <main className="flex-1">
        {isLoading ? (
          <div className="flex flex-col items-center justify-center h-[70vh] gap-3 text-slate-400">
            <Loader2 className="w-6 h-6 animate-spin text-indigo-500" />
            <span className="text-xs font-medium">Loading 9router native dashboard...</span>
          </div>
        ) : (
          <>
            {activeTab === 'connections' && (
              <ConnectionsView connections={connections} onRefresh={loadData} />
            )}
            {activeTab === 'combos' && (
              <CombosView combos={combos} onRefresh={loadData} />
            )}
            {activeTab === 'analytics' && <AnalyticsView />}
            {activeTab === 'keys' && <ApiKeysView apiKeys={apiKeys} onRefresh={loadData} />}
            {activeTab === 'settings' && (
              <SettingsView settings={settings} onRefresh={loadData} />
            )}
          </>
        )}
      </main>
    </div>
  )
}

export default App
