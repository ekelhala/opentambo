import { useCallback, useEffect, useState } from 'react'
import { DoorwayMark, GearIcon, MoonIcon, PlayIcon, SunIcon } from '@/components/icons'
import { LaunchDialog, type LaunchDraft } from '@/components/launch-dialog'
import { NavRail } from '@/components/nav-rail'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Toaster } from '@/components/ui/sonner'
import { api, type ModelFile, type Run, type State } from '@/lib/api'
import { bytes } from '@/lib/format'
import { useHashRoute } from '@/lib/router'
import { DashboardView, type History } from '@/views/dashboard'
import { DownloadsView } from '@/views/downloads'
import { FilesView } from '@/views/files'
import { GatewayView } from '@/views/gateway'
import { ServerDetail } from '@/views/server-detail'
import { ServersView } from '@/views/servers'
import { toast } from 'sonner'

const EMPTY_STATE: State = {
  settings: {
    models: '',
    binary: '',
    parallel_downloads: 0,
    prompt_cache_defaults: { enabled: false, max_bytes: 0, max_entry_bytes: 0, ttl_seconds: 0, min_reuse_tokens: 0 },
  },
  models: [],
  catalog: [],
  jobs: [],
  runs: [],
  profiles: {},
}

const HISTORY_LENGTH = 120

function readTheme(): 'dark' | 'light' {
  return localStorage.getItem('tambo-theme') === 'light' ? 'light' : 'dark'
}

export default function App() {
  const route = useHashRoute()
  const [state, setState] = useState<State | null>(null)
  const [connected, setConnected] = useState(false)
  const [draft, setDraft] = useState<LaunchDraft | null>(null)
  const [deleting, setDeleting] = useState<ModelFile | null>(null)
  const [theme, setTheme] = useState<'dark' | 'light'>(readTheme)
  const [history, setHistory] = useState<History>({ bandwidth: [], running: [], jobs: [] })

  const refresh = useCallback(async () => {
    try {
      const next = await api<State>('state')
      setState(next)
      setConnected(true)
      const push = (arr: number[], value: number) => [...arr, value].slice(-HISTORY_LENGTH)
      setHistory((h) => ({
        bandwidth: push(
          h.bandwidth,
          next.jobs.reduce((sum, job) => sum + job.bytes_per_second, 0),
        ),
        running: push(h.running, next.runs.filter((r) => r.status === 'running' || r.status === 'stopping').length),
        jobs: push(h.jobs, next.jobs.filter((j) => j.status === 'queued' || j.status === 'downloading').length),
      }))
    } catch {
      setConnected(false)
    }
  }, [])

  useEffect(() => {
    // polling the manager API is deliberate external-system sync; state lands
    // asynchronously after each fetch, so no cascading render is triggered
    // oxlint-disable-next-line react/set-state-in-effect
    refresh()
    const timer = setInterval(refresh, 2000)
    return () => clearInterval(timer)
  }, [refresh])

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    localStorage.setItem('tambo-theme', theme)
  }, [theme])

  const call = useCallback(
    async (path: string, method: string, body?: unknown, ok?: string) => {
      try {
        await api(path, method, body)
        if (ok) toast.success(ok)
      } catch (error) {
        toast.error(error instanceof Error ? error.message : String(error))
      }
      refresh()
    },
    [refresh],
  )

  const data = state ?? EMPTY_STATE
  const detailRun: Run | undefined = route.view === 'server' ? data.runs.find((r) => r.id === route.id) : undefined
  const running = data.runs.filter((r) => r.status === 'running' || r.status === 'stopping')

  return (
    <div className="panel-shell">
      <header className="masthead">
        <DoorwayMark lit />
        <h1 className="wordmark">tambo</h1>
        <span className="tag">The waystation for your models</span>
        <span className="grow" />
        <SettingsDialog state={state} connected={connected} />
        <Button onClick={() => setDraft({})} disabled={!state}>
          <PlayIcon />
          <span className="max-sm:hidden">Launch server</span>
          <span className="sm:hidden">Launch</span>
        </Button>
        <button
          className="theme-toggle"
          aria-label="Switch color theme"
          onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
        >
          {theme === 'dark' ? <MoonIcon /> : <SunIcon />}
          {theme === 'dark' ? 'Dark' : 'Light'}
        </button>
      </header>

      <NavRail
        active={route.view}
        connected={connected}
        counts={{
          servers: running.length,
          downloads: data.jobs.filter((j) => j.status === 'queued' || j.status === 'downloading').length,
          files: data.catalog.length,
          gateway: running.length,
        }}
      />

      <main className="stage">
        <div className="pane">
          {state ? (
            <>
              {route.view === 'dashboard' && <DashboardView state={data} history={history} />}
              {route.view === 'servers' && <ServersView runs={data.runs} />}
              {route.view === 'server' && (
                <ServerDetail
                  key={route.id}
                  run={detailRun}
                  onChanged={refresh}
                  onRelaunch={(run) => setDraft({ launch: run.launch })}
                />
              )}
              {route.view === 'downloads' && (
                <DownloadsView
                  state={data}
                  bandwidth={history.bandwidth}
                  onSubmit={(body) => call('downloads', 'POST', body, 'Queued')}
                  onCall={call}
                />
              )}
              {route.view === 'files' && (
                <FilesView state={data} onLaunch={(model) => setDraft({ model })} onDelete={setDeleting} />
              )}
              {route.view === 'gateway' && <GatewayView state={data} />}
            </>
          ) : (
            <p className="py-16 text-center text-sm text-[var(--t-text-2)]">
              {connected ? 'Loading…' : 'Cannot reach the tambo API. Is the manager running?'}
            </p>
          )}
        </div>
      </main>

      <Toaster theme={theme} />
      {state && draft && (
        <LaunchDialog
          key={JSON.stringify(draft)}
          state={state}
          draft={draft}
          onClose={() => setDraft(null)}
          onLaunched={refresh}
        />
      )}
      <AlertDialog open={!!deleting} onOpenChange={(open) => !open && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="dialog-title">Delete model?</AlertDialogTitle>
            <AlertDialogDescription className="mono mt-2 text-xs break-anywhere text-[var(--t-text-2)]">
              {deleting ? `${deleting.path} · ${bytes(deleting.size)}` : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                deleting && call(`models?path=${encodeURIComponent(deleting.path)}`, 'DELETE', undefined, 'Deleted')
              }
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function SettingsDialog({ state, connected }: { state: State | null; connected: boolean }) {
  return (
    <Dialog>
      <DialogTrigger asChild>
        <button className="icon-btn" aria-label="Configuration">
          <GearIcon />
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-[460px]">
        <DialogHeader>
          <DialogTitle className="dialog-title">Configuration</DialogTitle>
          <DialogDescription className="dialog-lede">
            How this manager was started. Files are the inventory; state lives beside them.
          </DialogDescription>
        </DialogHeader>
        {state && (
          <div className="grid gap-2 text-xs">
            <SettingRow label="Models" value={state.settings.models} />
            <SettingRow label="llama-server" value={state.settings.binary} />
            <SettingRow label="Downloads" value={`${state.settings.parallel_downloads} parallel`} />
            <SettingRow label="API" value={connected ? 'connected' : 'unreachable'} />
          </div>
        )}
        <div className="callout callout-info mt-5">
          <p>
            <strong>This panel deletes models and runs your runtime.</strong> It ships without authentication on
            purpose: access control belongs to your Pangolin policy, not to a second login nobody trusts.
          </p>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function SettingRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4">
      <span className="text-[var(--t-text-3)]">{label}</span>
      <span className="mono text-right break-anywhere">{value}</span>
    </div>
  )
}
