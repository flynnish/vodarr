import { useState, useEffect } from 'react'

const DEFAULT = {
  xtream: { url: '', username: '', password: '' },
  tmdb: { api_key: '', tvdb_api_key: '' },
  output: { path: '/data/strm', movies_dir: 'movies', series_dir: 'tv', mode: 'strm', max_concurrent_downloads: 1, download_delay: '30s', bandwidth_limit: '' },
  sync: { interval: '6h', on_startup: true, parallelism: 10, title_cleanup_patterns: [] },
  server: { newznab_port: 9091, qbit_port: 9092, web_port: 9090 },
  logging: { level: 'info' },
  arr: { instances: [] },
  update: { beta_channel: false },
}

const BLANK_ARR_INSTANCE = { name: '', type: 'sonarr', url: '', api_key: '' }

function Field({ label, hint, children }) {
  return (
    <div>
      <label className="block font-mono text-[11px] text-steel-400 uppercase tracking-widest mb-1.5">
        {label}
      </label>
      {children}
      {hint && <p className="mt-1 font-mono text-[10px] text-steel-500">{hint}</p>}
    </div>
  )
}

function TextInput({ value, onChange, type = 'text', placeholder, monospace }) {
  return (
    <input
      type={type}
      value={value}
      onChange={e => onChange(e.target.value)}
      placeholder={placeholder}
      className={[
        'w-full px-3 py-2 bg-void-800 border border-void-600 rounded text-steel-300 text-[13px]',
        monospace ? 'font-mono' : 'font-display',
      ].join(' ')}
    />
  )
}

function Toggle({ value, onChange }) {
  return (
    <button
      type="button"
      onClick={() => onChange(!value)}
      className={[
        'relative inline-flex h-5 w-9 items-center rounded-full transition-colors',
        value ? 'bg-lime-400/40 border border-lime-400/40' : 'bg-void-600 border border-void-500',
      ].join(' ')}
    >
      <span
        className={[
          'inline-block h-3.5 w-3.5 transform rounded-full transition-transform',
          value ? 'translate-x-4 bg-lime-400' : 'translate-x-1 bg-steel-500',
        ].join(' ')}
      />
    </button>
  )
}

function Section({ title, children }) {
  return (
    <div className="border border-void-600 rounded-lg overflow-hidden bg-void-800/60">
      <div className="px-5 py-3 border-b border-void-600 bg-void-700/40">
        <h2 className="font-display font-600 text-sm text-steel-300 uppercase tracking-widest">
          {title}
        </h2>
      </div>
      <div className="p-5 space-y-5">
        {children}
      </div>
    </div>
  )
}

function StatusRow({ label, ok, fail, failMsg }) {
  return (
    <div className="flex items-center justify-between">
      <span className="font-mono text-[11px] text-steel-500">{label}</span>
      {ok && <span className="font-mono text-[11px] text-lime-400">✓ OK</span>}
      {fail && (
        <span className="font-mono text-[11px] text-yellow-400" title={failMsg || ''}>
          ✗ {failMsg || 'Not configured'}
        </span>
      )}
      {!ok && !fail && <span className="font-mono text-[11px] text-steel-600">—</span>}
    </div>
  )
}

function TestButton({ onClick, loading, success, error }) {
  return (
    <div className="flex items-center gap-3 pt-1">
      <button
        type="button"
        onClick={onClick}
        disabled={loading}
        className="px-4 py-1.5 bg-void-600 border border-void-500 text-steel-400 rounded font-mono text-[12px] hover:bg-void-500 hover:text-steel-300 transition-all disabled:opacity-40"
      >
        {loading ? 'Testing…' : 'Test Connection'}
      </button>
      {success && (
        <span className="font-mono text-[12px] text-lime-400 animate-fade-up">✓ Connected</span>
      )}
      {error && (
        <span className="font-mono text-[12px] text-red-400 animate-fade-up">{error}</span>
      )}
    </div>
  )
}

// RepairImports finishes past imports the webhook never handled: it places
// the .strm in the library and removes the .mkv stub for each VODarr import
// in Sonarr/Radarr's history. Safe to run repeatedly.
function RepairImports() {
  const [state, setState] = useState({ loading: false, results: null, error: null })

  const run = async () => {
    setState({ loading: true, results: null, error: null })
    try {
      const res = await fetch('/api/arr/repair', { method: 'POST' })
      const data = await res.json()
      if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
      setState({ loading: false, results: data.results, error: null })
    } catch (e) {
      setState({ loading: false, results: null, error: e.message })
    }
  }

  return (
    <div className="border-t border-void-600 pt-4 space-y-2">
      <p className="font-mono text-[11px] text-steel-500">
        Repair past imports: for every VODarr import in Sonarr/Radarr's history that still has its .mkv stub (because the
        webhook missed it or couldn't see the library), place the .strm, remove the stub, then unmonitor and rescan it.
        Safe to run more than once. Save and restart first if you just added an instance.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          onClick={run}
          disabled={state.loading}
          className="px-4 py-1.5 bg-void-600 border border-void-500 text-steel-400 rounded font-mono text-[12px] hover:bg-void-500 hover:text-steel-300 transition-all disabled:opacity-40"
        >
          {state.loading ? 'Repairing…' : 'Repair past imports'}
        </button>
        {state.error && <span className="font-mono text-[12px] text-red-400">{state.error}</span>}
      </div>
      {state.results && state.results.map(r => (
        <div key={r.instance} className="font-mono text-[11px] text-steel-400">
          <span className="text-steel-300">{r.instance}:</span>{' '}
          {r.error ? <span className="text-red-400">{r.error}</span> : (
            <>
              <span className="text-lime-400">{r.repaired} repaired</span> · {r.done} already done
              {r.not_visible > 0 && <span className="text-amber-400"> · {r.not_visible} not visible (library not mounted into VODarr at arr's path)</span>}
              {r.no_strm > 0 && <span className="text-amber-400"> · {r.no_strm} without a .strm (VODarr's output folder path differs between containers)</span>}
              {r.failed > 0 && <span className="text-red-400"> · {r.failed} failed (see log)</span>}
            </>
          )}
        </div>
      ))}
    </div>
  )
}

// ProviderGroups lists the provider's categories so whole groups (e.g. every
// Spanish one) can be left out of the index. It saves on its own, outside
// the main config form, and needs no restart.
function ProviderGroups() {
  const [groups, setGroups] = useState(null)
  const [excluded, setExcluded] = useState(new Set())
  const [savedExcluded, setSavedExcluded] = useState(new Set())
  const [kind, setKind] = useState('movie')
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState(null)
  const [error, setError] = useState(null)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await fetch('/api/categories')
      const data = await res.json()
      if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
      const ex = new Set(data.categories.filter(c => c.excluded).map(c => c.key))
      setGroups(data.categories)
      setExcluded(ex)
      setSavedExcluded(new Set(ex))
    } catch (e) {
      setError(e.message)
    } finally {
      setLoading(false)
    }
  }

  const shown = (groups || []).filter(g =>
    g.type === kind && (!query || g.name.toLowerCase().includes(query.toLowerCase())))

  const toggle = key => setExcluded(prev => {
    const next = new Set(prev)
    next.has(key) ? next.delete(key) : next.add(key)
    return next
  })
  const setShown = exclude => setExcluded(prev => {
    const next = new Set(prev)
    shown.forEach(g => exclude ? next.add(g.key) : next.delete(g.key))
    return next
  })

  const dirty = excluded.size !== savedExcluded.size || [...excluded].some(k => !savedExcluded.has(k))

  const save = async () => {
    setSaving(true)
    setError(null)
    setMessage(null)
    try {
      const res = await fetch('/api/categories', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ excluded: [...excluded] }),
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
      setSavedExcluded(new Set(excluded))
      setMessage(`Saved. ${data.removed} item${data.removed === 1 ? '' : 's'} removed from search now; the next sync applies it fully. Groups you include again come back on the next sync.`)
    } catch (e) {
      setError(e.message)
    } finally {
      setSaving(false)
    }
  }

  if (!groups) {
    return (
      <div className="space-y-3">
        <p className="font-mono text-[11px] text-steel-500">
          Exclude provider groups you never want used, e.g. every Spanish group, so a show that exists in several
          languages is only found in the ones you keep.
        </p>
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={load}
            disabled={loading}
            className="px-4 py-1.5 bg-void-600 border border-void-500 text-steel-400 rounded font-mono text-[12px] hover:bg-void-500 hover:text-steel-300 transition-all disabled:opacity-40"
          >
            {loading ? 'Loading groups…' : 'Load groups from provider'}
          </button>
          {error && <span className="font-mono text-[12px] text-red-400">{error}</span>}
        </div>
      </div>
    )
  }

  const excludedOfKind = groups.filter(g => g.type === kind && excluded.has(g.key)).length

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {[{ id: 'movie', label: 'Movies' }, { id: 'series', label: 'Series' }].map(({ id, label }) => (
          <button
            key={id}
            type="button"
            onClick={() => setKind(id)}
            className={[
              'px-3 py-1 rounded font-mono text-[11px] border transition-all',
              kind === id ? 'bg-lime-400/10 text-lime-400 border-lime-400/20' : 'text-steel-400 border-void-600 hover:text-steel-300',
            ].join(' ')}
          >
            {label}
          </button>
        ))}
        <input
          type="text"
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder="Filter groups, e.g. ES or Spanish"
          className="flex-1 min-w-[12rem] px-3 py-1 bg-void-800 border border-void-600 rounded font-mono text-[12px] text-steel-300 placeholder-steel-500"
        />
      </div>
      <div className="flex flex-wrap items-center gap-3 font-mono text-[11px] text-steel-500">
        <span>{shown.length} shown · {excludedOfKind} excluded</span>
        <button type="button" onClick={() => setShown(true)} className="text-steel-400 hover:text-red-400">Exclude all shown</button>
        <button type="button" onClick={() => setShown(false)} className="text-steel-400 hover:text-lime-400">Include all shown</button>
      </div>
      <div className="max-h-80 overflow-y-auto border border-void-600 rounded divide-y divide-void-600/50">
        {shown.length === 0 ? (
          <p className="px-3 py-4 font-mono text-[11px] text-steel-500">No groups match.</p>
        ) : shown.map(g => {
          const isExcluded = excluded.has(g.key)
          return (
            <label key={g.key} className="flex items-center gap-3 px-3 py-1.5 cursor-pointer hover:bg-void-700/40">
              <input type="checkbox" checked={!isExcluded} onChange={() => toggle(g.key)} className="accent-lime-400" />
              <span className={`flex-1 min-w-0 truncate font-display text-[13px] ${isExcluded ? 'text-steel-600 line-through' : 'text-steel-300'}`}>
                {g.name}
              </span>
              <span className="font-mono text-[10px] text-steel-500">{isExcluded ? 'excluded' : `${g.count} indexed`}</span>
            </label>
          )
        })}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <button
          type="button"
          onClick={save}
          disabled={saving || !dirty}
          className="px-4 py-1.5 bg-lime-400/10 border border-lime-400/30 text-lime-400 rounded font-mono text-[12px] hover:bg-lime-400/20 transition-all disabled:opacity-40"
        >
          {saving ? 'Saving…' : 'Save groups'}
        </button>
        {message && <span className="font-mono text-[11px] text-lime-400">{message}</span>}
        {error && <span className="font-mono text-[11px] text-red-400">{error}</span>}
      </div>
    </div>
  )
}

export default function Settings() {
  const [cfg, setCfg] = useState(DEFAULT)
  const [patternsText, setPatternsText] = useState('')
  const [saved, setSaved] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState(null)
  const [loadError, setLoadError] = useState(null)
  const [restartRequired, setRestartRequired] = useState(false)
  const [restarting, setRestarting] = useState(false)

  const [logDownloading, setLogDownloading] = useState(false)
  const [updateInfo, setUpdateInfo] = useState(null)
  const [updateLoading, setUpdateLoading] = useState(false)

  const [xtreamTest, setXtreamTest] = useState({ loading: false, success: false, error: null })
  const [tmdbTest, setTmdbTest] = useState({ loading: false, success: false, error: null })
  const [tvdbTest, setTvdbTest] = useState({ loading: false, success: false, error: null })

  const [arrStatus, setArrStatus] = useState(null)
  const [arrTestState, setArrTestState] = useState({}) // keyed by instance name
  const [arrSetupState, setArrSetupState] = useState({}) // keyed by instance name

  useEffect(() => {
    fetch('/api/config')
      .then(r => r.json())
      .then(data => {
        setCfg(prev => deepMerge(prev, data))
        setPatternsText((data.sync?.title_cleanup_patterns || []).join('\n'))
      })
      .catch(e => setLoadError(e.message))
    fetchArrStatus()
    fetchUpdateInfo()
  }, [])

  const fetchArrStatus = () => {
    fetch('/api/arr/status')
      .then(r => r.json())
      .then(data => setArrStatus(data))
      .catch(() => {}) // non-fatal
  }

  const fetchUpdateInfo = () => {
    setUpdateLoading(true)
    fetch('/api/update')
      .then(r => r.json())
      .then(data => setUpdateInfo(data))
      .catch(() => {})
      .finally(() => setUpdateLoading(false))
  }

  const addArrInstance = () => {
    setCfg(prev => ({
      ...prev,
      arr: { instances: [...(prev.arr?.instances || []), { ...BLANK_ARR_INSTANCE }] },
    }))
  }

  const removeArrInstance = idx => {
    setCfg(prev => ({
      ...prev,
      arr: { instances: prev.arr.instances.filter((_, i) => i !== idx) },
    }))
  }

  const setArrInstance = (idx, field, value) => {
    setCfg(prev => {
      const instances = prev.arr.instances.map((inst, i) =>
        i === idx ? { ...inst, [field]: value } : inst
      )
      return { ...prev, arr: { instances } }
    })
  }

  const testArrInstance = async (name, url, apiKey) => {
    setArrTestState(s => ({ ...s, [name]: { loading: true, success: false, error: null } }))
    try {
      const res = await fetch('/api/arr/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url, api_key: apiKey }),
      })
      const data = await res.json()
      if (data.success) {
        setArrTestState(s => ({ ...s, [name]: { loading: false, success: true, error: null } }))
        setTimeout(() => setArrTestState(s => ({ ...s, [name]: { ...s[name], success: false } })), 5000)
        await saveConfig(buildPayload())
        fetchArrStatus()
      } else {
        setArrTestState(s => ({ ...s, [name]: { loading: false, success: false, error: data.error || 'Connection failed' } }))
        setTimeout(() => setArrTestState(s => ({ ...s, [name]: { ...s[name], error: null } })), 5000)
      }
    } catch (e) {
      setArrTestState(s => ({ ...s, [name]: { loading: false, success: false, error: e.message } }))
      setTimeout(() => setArrTestState(s => ({ ...s, [name]: { ...s[name], error: null } })), 5000)
    }
  }

  const handleArrSetup = async name => {
    if (!window.confirm(`This will register a webhook, indexer, and download client in "${name}", and remove strm from its Import Extra Files extensions. Proceed?`)) return
    setArrSetupState(s => ({ ...s, [name]: { loading: true, error: null, success: false } }))
    try {
      const res = await fetch('/api/arr/setup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ instance: name }),
      })
      const data = await res.json()
      if (!res.ok) {
        setArrSetupState(s => ({ ...s, [name]: { loading: false, success: false, error: data.error || `HTTP ${res.status}` } }))
        return
      }
      const failures = Object.entries(data).filter(([, v]) => v?.success === false).map(([k, v]) => `${k}: ${v.error || 'failed'}`)
      const allOk = failures.length === 0
      setArrSetupState(s => ({
        ...s,
        [name]: { loading: false, success: allOk, error: allOk ? null : failures.join('; ') },
      }))
      fetchArrStatus()
    } catch (e) {
      setArrSetupState(s => ({ ...s, [name]: { loading: false, success: false, error: e.message } }))
    }
  }

  // Build the full config payload, parsing patternsText into an array.
  const buildPayload = () => ({
    ...cfg,
    sync: {
      ...cfg.sync,
      title_cleanup_patterns: patternsText.split('\n').filter(l => l.trim()),
    },
  })

  const set = (path, value) => {
    setCfg(prev => {
      const next = structuredClone(prev)
      const parts = path.split('.')
      let obj = next
      for (let i = 0; i < parts.length - 1; i++) obj = obj[parts[i]]
      obj[parts[parts.length - 1]] = value
      return next
    })
  }

  const saveConfig = async (currentCfg) => {
    setSaving(true)
    setSaveError(null)
    try {
      const res = await fetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(currentCfg),
      })
      const data = await res.json()
      if (data.error) {
        setSaveError(data.error)
      } else {
        setSaved(true)
        setTimeout(() => setSaved(false), 3000)
        if (data.restart_required) {
          setRestartRequired(true)
        }
      }
    } catch (e) {
      setSaveError(e.message)
    }
    setSaving(false)
  }

  const handleSave = async e => {
    e.preventDefault()
    await saveConfig(buildPayload())
  }

  const handleRestart = async () => {
    setRestarting(true)
    try {
      await fetch('/api/restart', { method: 'POST' })
    } catch (_) {
      // expected — server closes connection as it exits
    }
    // Poll /api/health until the server is back, then reload
    const poll = () => {
      fetch('/api/health')
        .then(r => r.ok ? window.location.reload() : setTimeout(poll, 500))
        .catch(() => setTimeout(poll, 500))
    }
    setTimeout(poll, 1000)
  }

  const handleBetaToggle = async (v) => {
    set('update.beta_channel', v)
    const next = structuredClone(cfg)
    next.update = { beta_channel: v }
    next.sync.title_cleanup_patterns = patternsText.split('\n').filter(l => l.trim())
    await saveConfig(next)
    fetchUpdateInfo()
  }

  const downloadLogs = async () => {
    setLogDownloading(true)
    try {
      const res = await fetch('/api/logs/download')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `vodarr-${new Date().toISOString().slice(0, 10)}.log`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (e) {
      console.error('log download failed', e)
    } finally {
      setLogDownloading(false)
    }
  }

  const testXtream = async () => {
    setXtreamTest({ loading: true, success: false, error: null })
    try {
      const res = await fetch('/api/test-xtream', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(cfg.xtream),
      })
      const data = await res.json()
      if (data.success) {
        setXtreamTest({ loading: false, success: true, error: null })
        setTimeout(() => setXtreamTest(s => ({ ...s, success: false })), 5000)
        await saveConfig(buildPayload())
      } else {
        setXtreamTest({ loading: false, success: false, error: data.error || 'Connection failed' })
        setTimeout(() => setXtreamTest(s => ({ ...s, error: null })), 5000)
      }
    } catch (e) {
      setXtreamTest({ loading: false, success: false, error: e.message })
      setTimeout(() => setXtreamTest(s => ({ ...s, error: null })), 5000)
    }
  }

  const testTMDB = async () => {
    setTmdbTest({ loading: true, success: false, error: null })
    try {
      const res = await fetch('/api/test-tmdb', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ api_key: cfg.tmdb.api_key }),
      })
      const data = await res.json()
      if (data.success) {
        setTmdbTest({ loading: false, success: true, error: null })
        setTimeout(() => setTmdbTest(s => ({ ...s, success: false })), 5000)
        await saveConfig(buildPayload())
      } else {
        setTmdbTest({ loading: false, success: false, error: data.error || 'Connection failed' })
        setTimeout(() => setTmdbTest(s => ({ ...s, error: null })), 5000)
      }
    } catch (e) {
      setTmdbTest({ loading: false, success: false, error: e.message })
      setTimeout(() => setTmdbTest(s => ({ ...s, error: null })), 5000)
    }
  }

  const testTVDB = async () => {
    setTvdbTest({ loading: true, success: false, error: null })
    try {
      const res = await fetch('/api/test-tvdb', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tvdb_api_key: cfg.tmdb.tvdb_api_key }),
      })
      const data = await res.json()
      if (data.success) {
        setTvdbTest({ loading: false, success: true, error: null })
        setTimeout(() => setTvdbTest(s => ({ ...s, success: false })), 5000)
        await saveConfig(buildPayload())
      } else {
        setTvdbTest({ loading: false, success: false, error: data.error || 'Connection failed' })
        setTimeout(() => setTvdbTest(s => ({ ...s, error: null })), 5000)
      }
    } catch (e) {
      setTvdbTest({ loading: false, success: false, error: e.message })
      setTimeout(() => setTvdbTest(s => ({ ...s, error: null })), 5000)
    }
  }

  return (
    <div className="p-8">
      <div className="mb-8 animate-fade-up animate-fade-up-1">
        <h1 className="font-display font-700 text-2xl text-steel-300 tracking-tight">Settings</h1>
        <p className="mt-1 font-mono text-[12px] text-steel-500">
          Configuration is applied on save — credentials are stored server-side only
        </p>
        {loadError && (
          <p className="mt-2 font-mono text-[11px] text-yellow-400/80">
            Note: Could not load current config ({loadError}). Showing defaults.
          </p>
        )}
        {restartRequired && !restarting && (
          <div className="mt-3 px-4 py-2.5 bg-yellow-400/10 border border-yellow-400/30 rounded font-mono text-[12px] text-yellow-400 flex items-center justify-between gap-4">
            <span>Restart required — changes take effect after restarting VODarr.</span>
            <button
              type="button"
              onClick={handleRestart}
              className="px-3 py-1 bg-yellow-400/20 border border-yellow-400/40 rounded hover:bg-yellow-400/30 transition-all whitespace-nowrap"
            >
              Restart Now
            </button>
          </div>
        )}
        {restarting && (
          <div className="mt-3 px-4 py-2.5 bg-void-700 border border-void-500 rounded font-mono text-[12px] text-steel-400 flex items-center gap-2">
            <span className="inline-block w-2 h-2 rounded-full bg-blue-400 animate-pulse" />
            Restarting… page will reload automatically.
          </div>
        )}
      </div>

      <form onSubmit={handleSave} className="space-y-5 max-w-2xl">

        {/* Xtream */}
        <div className="animate-fade-up animate-fade-up-1">
          <Section title="Xtream Provider">
            <Field label="Server URL" hint="Base URL of your Xtream provider, no trailing slash">
              <TextInput
                value={cfg.xtream.url}
                onChange={v => set('xtream.url', v)}
                placeholder="http://provider.example.com"
                monospace
              />
            </Field>
            <div className="grid grid-cols-2 gap-4">
              <Field label="Username">
                <TextInput value={cfg.xtream.username} onChange={v => set('xtream.username', v)} monospace />
              </Field>
              <Field label="Password">
                <TextInput value={cfg.xtream.password} onChange={v => set('xtream.password', v)} type="password" monospace />
              </Field>
            </div>
            <TestButton
              onClick={testXtream}
              loading={xtreamTest.loading}
              success={xtreamTest.success}
              error={xtreamTest.error}
            />
          </Section>
        </div>

        {/* Provider groups */}
        <div className="animate-fade-up animate-fade-up-1">
          <Section title="Provider Groups">
            <ProviderGroups />
          </Section>
        </div>

        {/* Arr Integration */}
        <div className="animate-fade-up animate-fade-up-2">
          <Section title="Arr Integration">
            <p className="font-mono text-[11px] text-steel-500">
              Connect Sonarr/Radarr instances. Test the connection first, then use Auto-Configure to register the indexer, download client and webhook in one click. VODarr places the .strm in your library itself after import, so strm must not be an Import Extra Files extension.
            </p>
            <p className="font-mono text-[11px] text-steel-500/90">
              Auto-Configure usually takes about 30-90 seconds. If Arr is busy validating indexers, it can take up to ~120 seconds.
            </p>
            {(cfg.arr?.instances || []).map((inst, idx) => {
              const statusInst = arrStatus?.instances?.find(s => s.name === inst.name)
              const arrTestSt = arrTestState[inst.name] || {}
              const setupSt = arrSetupState[inst.name] || {}
              const apiOk = statusInst?.reachable === true
              const apiFail = statusInst?.reachable === false
              const strmIsExtra = statusInst?.importExtraFiles && (statusInst?.extraFileExtensions || '').split(',').some(e => e.trim().replace(/^\./, '').toLowerCase() === 'strm')
              const webhookOk = statusInst?.webhookConfigured && !strmIsExtra
              const webhookIssues = statusInst && !webhookOk ? (statusInst.issues.filter(i => i !== 'unreachable: ' + (statusInst.issues[0] || ''))) : []
              return (
                <div key={idx} className="border border-void-600 rounded p-4 space-y-3">
                  <div className="flex items-center justify-end">
                    <button
                      type="button"
                      onClick={() => removeArrInstance(idx)}
                      className="font-mono text-[11px] text-steel-500 hover:text-red-400 transition-colors"
                    >
                      Remove
                    </button>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <Field label="Name">
                      <TextInput value={inst.name} onChange={v => setArrInstance(idx, 'name', v)} monospace placeholder="Sonarr" />
                    </Field>
                    <Field label="Type">
                      <select
                        value={inst.type}
                        onChange={e => setArrInstance(idx, 'type', e.target.value)}
                        className="w-full px-3 py-2 bg-void-800 border border-void-600 rounded font-mono text-[13px] text-steel-300"
                      >
                        <option value="sonarr">sonarr</option>
                        <option value="radarr">radarr</option>
                      </select>
                    </Field>
                  </div>
                  <Field label="URL">
                    <TextInput value={inst.url} onChange={v => setArrInstance(idx, 'url', v)} monospace placeholder="http://sonarr:8989" />
                  </Field>
                  <Field label="API Key">
                    <TextInput value={inst.api_key} onChange={v => setArrInstance(idx, 'api_key', v)} type="password" monospace placeholder="••••••••••••••••" />
                  </Field>

                  {/* Status rows */}
                  {statusInst && (
                    <div className="border-t border-void-600 pt-3 space-y-1.5">
                      <StatusRow
                        label="API Connection"
                        ok={apiOk}
                        fail={apiFail}
                        failMsg={statusInst.issues.find(i => i.startsWith('unreachable'))}
                      />
                      <StatusRow
                        label="Webhook & .strm import"
                        ok={webhookOk}
                        fail={!webhookOk && apiOk}
                        failMsg={statusInst.issues.filter(i => !i.startsWith('unreachable')).join(' · ')}
                      />
                      <StatusRow
                        label="Indexer registered"
                        ok={statusInst?.indexerConfigured}
                        fail={!statusInst?.indexerConfigured && apiOk}
                        failMsg="VODarr indexer not registered"
                      />
                      <StatusRow
                        label="Download client"
                        ok={statusInst?.downloadClientConfigured}
                        fail={!statusInst?.downloadClientConfigured && apiOk}
                        failMsg="VODarr download client not registered"
                      />
                    </div>
                  )}

                  <div className="flex items-center gap-3 pt-1 flex-wrap">
                    <TestButton
                      onClick={() => testArrInstance(inst.name, inst.url, inst.api_key)}
                      loading={arrTestSt.loading}
                      success={arrTestSt.success}
                      error={arrTestSt.error}
                    />
                    <button
                      type="button"
                      onClick={() => handleArrSetup(inst.name)}
                      disabled={setupSt.loading || !apiOk}
                      className="px-4 py-1.5 bg-void-600 border border-void-500 text-steel-400 rounded font-mono text-[12px] hover:bg-void-500 hover:text-steel-300 transition-all disabled:opacity-40"
                    >
                      {setupSt.loading ? 'Configuring…' : 'Auto-Configure'}
                    </button>
                    {setupSt.success && <span className="font-mono text-[12px] text-lime-400">✓ Done</span>}
                    {setupSt.error && <span className="font-mono text-[12px] text-red-400 break-all">✗ {setupSt.error}</span>}
                  </div>
                </div>
              )
            })}
            <button
              type="button"
              onClick={addArrInstance}
              className="w-full py-2 border border-dashed border-void-500 text-steel-500 rounded font-mono text-[12px] hover:border-void-400 hover:text-steel-400 transition-all"
            >
              + Add Instance
            </button>
            <RepairImports />
          </Section>
        </div>

        {/* TMDB */}
        <div className="animate-fade-up animate-fade-up-2">
          <Section title="TMDB">
            <Field label="API Key" hint="Get your free key at themoviedb.org/settings/api">
              <TextInput
                value={cfg.tmdb.api_key}
                onChange={v => set('tmdb.api_key', v)}
                type="password"
                placeholder="••••••••••••••••"
                monospace
              />
            </Field>
            <TestButton
              onClick={testTMDB}
              loading={tmdbTest.loading}
              success={tmdbTest.success}
              error={tmdbTest.error}
            />
          </Section>
        </div>

        {/* TVDB */}
        <div className="animate-fade-up animate-fade-up-2">
          <Section title="TVDB">
            <Field label="API Key" hint="Optional — enables direct TVDB search for series TMDB can't cross-link. Get a free key at thetvdb.com/api-information">
              <TextInput
                value={cfg.tmdb.tvdb_api_key}
                onChange={v => set('tmdb.tvdb_api_key', v)}
                type="password"
                placeholder="••••••••••••••••"
                monospace
              />
            </Field>
            <TestButton
              onClick={testTVDB}
              loading={tvdbTest.loading}
              success={tvdbTest.success}
              error={tvdbTest.error}
            />
          </Section>
        </div>

        {/* Output */}
        <div className="animate-fade-up animate-fade-up-3">
          <Section title="Output">
            <Field label="Mode" hint={cfg.output.mode === 'download'
              ? 'Downloads actual media files from the provider. Uses real disk space.'
              : 'Writes .strm pointer files. No disk space used — streams directly from provider.'}>
              <select
                value={cfg.output.mode || 'strm'}
                onChange={e => set('output.mode', e.target.value)}
                className="w-full px-3 py-2 bg-void-800 border border-void-600 rounded font-mono text-[13px] text-steel-300"
              >
                <option value="strm">strm — stream from provider</option>
                <option value="download">download — save files to disk</option>
              </select>
            </Field>
            {cfg.output.mode === 'download' && (
              <>
                <div className="grid grid-cols-3 gap-4">
                  <Field label="Max Concurrent Downloads" hint="1-2 recommended to avoid provider bans.">
                    <TextInput
                      value={cfg.output.max_concurrent_downloads}
                      onChange={v => set('output.max_concurrent_downloads', parseInt(v, 10) || 1)}
                      type="number"
                      monospace
                    />
                  </Field>
                  <Field label="Delay Between Downloads" hint="Pause after each download. Prevents back-to-back pattern that looks automated.">
                    <TextInput
                      value={cfg.output.download_delay}
                      onChange={v => set('output.download_delay', v)}
                      placeholder="30s"
                      monospace
                    />
                  </Field>
                  <Field label="Bandwidth Limit" hint="Max speed per download, e.g. 20M (MB/s). Empty = unlimited.">
                    <TextInput
                      value={cfg.output.bandwidth_limit}
                      onChange={v => set('output.bandwidth_limit', v)}
                      placeholder="unlimited"
                      monospace
                    />
                  </Field>
                </div>
                <p className="font-mono text-[10px] text-steel-500 -mt-1">
                  Downloads use a VLC-like user agent to blend in with normal streaming traffic.
                </p>
              </>
            )}
            <Field label="Base Path" hint="Root directory where files will be written">
              <TextInput value={cfg.output.path} onChange={v => set('output.path', v)} monospace />
            </Field>
            <div className="grid grid-cols-2 gap-4">
              <Field label="Movies Subdirectory">
                <TextInput value={cfg.output.movies_dir} onChange={v => set('output.movies_dir', v)} monospace />
              </Field>
              <Field label="Series Subdirectory">
                <TextInput value={cfg.output.series_dir} onChange={v => set('output.series_dir', v)} monospace />
              </Field>
            </div>
          </Section>
        </div>

        {/* Sync */}
        <div className="animate-fade-up animate-fade-up-4">
          <Section title="Sync Schedule">
            <div className="grid grid-cols-2 gap-4">
              <Field label="Interval" hint="Go duration format: 6h, 12h, 24h, 1h30m">
                <TextInput value={cfg.sync.interval} onChange={v => set('sync.interval', v)} monospace />
              </Field>
              <Field label="Parallelism" hint="Concurrent workers for series fetch and TMDB enrichment (1–20)">
                <TextInput
                  value={String(cfg.sync.parallelism)}
                  onChange={v => set('sync.parallelism', Math.min(20, Math.max(1, parseInt(v) || 1)))}
                  monospace
                />
              </Field>
            </div>
            <div className="flex items-center justify-between">
              <Field label="Sync on Startup">
                <span className="font-mono text-[11px] text-steel-500">
                  Run a full sync when VODarr starts
                </span>
              </Field>
              <Toggle value={cfg.sync.on_startup} onChange={v => set('sync.on_startup', v)} />
            </div>
            <Field
              label="Title Cleanup Patterns"
              hint="One regex per line. Matched text is removed from stream names before TMDB search."
            >
              <textarea
                value={patternsText}
                onChange={e => setPatternsText(e.target.value)}
                placeholder={'\\s*\\(DUBBED\\)\n\\s*\\[EXTENDED\\]'}
                rows={4}
                className="w-full px-3 py-2 bg-void-800 border border-void-600 rounded text-steel-300 text-[13px] font-mono resize-y"
              />
            </Field>
          </Section>
        </div>

        {/* Ports */}
        <div className="animate-fade-up animate-fade-up-4">
          <Section title="Server Ports">
            <div className="grid grid-cols-3 gap-4">
              <Field label="Newznab Port" hint="Indexer API">
                <TextInput
                  value={String(cfg.server.newznab_port)}
                  onChange={v => set('server.newznab_port', parseInt(v) || 9091)}
                  monospace
                />
              </Field>
              <Field label="qBit Port" hint="Download client">
                <TextInput
                  value={String(cfg.server.qbit_port)}
                  onChange={v => set('server.qbit_port', parseInt(v) || 9092)}
                  monospace
                />
              </Field>
              <Field label="Web Port" hint="This UI">
                <TextInput
                  value={String(cfg.server.web_port)}
                  onChange={v => set('server.web_port', parseInt(v) || 9090)}
                  monospace
                />
              </Field>
            </div>
          </Section>
        </div>

        {/* Logging */}
        <div className="animate-fade-up animate-fade-up-4">
          <Section title="Logging">
            <Field label="Log Level">
              <select
                value={cfg.logging.level}
                onChange={e => set('logging.level', e.target.value)}
                className="w-full px-3 py-2 bg-void-800 border border-void-600 rounded font-mono text-[13px] text-steel-300"
              >
                <option value="debug">debug</option>
                <option value="info">info</option>
                <option value="warn">warn</option>
                <option value="error">error</option>
              </select>
            </Field>
            <div className="pt-1">
              <button
                type="button"
                onClick={downloadLogs}
                disabled={logDownloading}
                className="px-4 py-1.5 bg-void-600 border border-void-500 text-steel-400 rounded font-mono text-[12px] hover:bg-void-500 hover:text-steel-300 transition-all disabled:opacity-40"
              >
                {logDownloading ? 'Preparing…' : 'Download Logs'}
              </button>
              <p className="mt-1 font-mono text-[10px] text-steel-500">
                Last ~5 000 lines. Credentials and IP addresses are redacted.
              </p>
            </div>
          </Section>
        </div>

        {/* Updates */}
        <div className="animate-fade-up animate-fade-up-4">
          <Section title="Updates">
            <div className="flex items-center justify-between">
              <Field label="Beta Channel">
                <span className="font-mono text-[11px] text-steel-500">
                  Receive pre-release builds instead of stable releases
                </span>
              </Field>
              <Toggle value={cfg.update?.beta_channel ?? false} onChange={handleBetaToggle} />
            </div>

            {updateLoading && (
              <p className="font-mono text-[11px] text-steel-500">Checking for updates…</p>
            )}

            {updateInfo && !updateLoading && (
              <div className="border-t border-void-600 pt-4 space-y-2">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-[11px] text-steel-500">Current version</span>
                  <span className="font-mono text-[11px] text-steel-300">{updateInfo.current_version || '—'}</span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="font-mono text-[11px] text-steel-500">Latest available</span>
                  <span className="font-mono text-[11px] text-steel-300">
                    {updateInfo.latest_version
                      ? `${updateInfo.latest_version}${updateInfo.is_prerelease ? ' (beta)' : ''}`
                      : '—'}
                  </span>
                </div>
                {updateInfo.update_available && (
                  <div className="mt-2 px-3 py-2 bg-lime-400/10 border border-lime-400/30 rounded">
                    <p className="font-mono text-[11px] text-lime-400">Update available — pull the new image:</p>
                    <code className="block mt-1 font-mono text-[11px] text-steel-300 break-all">
                      docker pull {updateInfo.image_tag}
                    </code>
                  </div>
                )}
                {updateInfo.error && (
                  <p className="font-mono text-[11px] text-yellow-400/80">
                    Could not check for updates: {updateInfo.error}
                  </p>
                )}
              </div>
            )}
          </Section>
        </div>

        {/* Save */}
        <div className="flex items-center gap-4 pt-2">
          <button
            type="submit"
            disabled={saving}
            className="px-6 py-2.5 bg-lime-400/10 border border-lime-400/30 text-lime-400 rounded font-mono text-[13px] hover:bg-lime-400/20 hover:border-lime-400/50 transition-all disabled:opacity-40"
          >
            {saving ? 'Saving…' : 'Save Configuration'}
          </button>
          {saved && (
            <span className="font-mono text-[12px] text-lime-400 animate-fade-up">
              ✓ Saved
            </span>
          )}
          {saveError && (
            <span className="font-mono text-[12px] text-red-400 animate-fade-up">
              {saveError}
            </span>
          )}
        </div>
      </form>
    </div>
  )
}

function deepMerge(target, source) {
  const out = { ...target }
  for (const key of Object.keys(source || {})) {
    if (source[key] && typeof source[key] === 'object' && !Array.isArray(source[key])) {
      out[key] = deepMerge(target[key] || {}, source[key])
    } else if (source[key] !== undefined && source[key] !== null && source[key] !== '') {
      out[key] = source[key]
    }
  }
  return out
}
