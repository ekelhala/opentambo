import { useCallback, useEffect, useState } from 'react'
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
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Separator } from '@/components/ui/separator'
import { Toaster } from '@/components/ui/sonner'
import { api, type ModelFile, type Run, type State } from '@/lib/api'
import { bytes } from '@/lib/format'
import { navigate, useHashRoute } from '@/lib/router'
import { DashboardView, type History } from '@/views/dashboard'
import { DownloadsView } from '@/views/downloads'
import { FilesView } from '@/views/files'
import { ServerDetail } from '@/views/server-detail'
import { ServersView } from '@/views/servers'
import { InfoIcon, MoonIcon, PlayIcon, SunIcon } from 'lucide-react'
import { toast } from 'sonner'

const EMPTY_STATE: State = {
  settings: { models: '', binary: '', parallel_downloads: 0 },
  models: [],
  jobs: [],
  runs: [],
  profiles: {},
}

const TITLES: Record<string, [string, string]> = {
  dashboard: ['Dashboard', 'Overview of servers, transfers and model storage'],
  servers: ['Servers', 'Managed llama-server processes, logs and launch configuration'],
  downloads: ['Downloads', 'Hugging Face transfers with resume, cancel and bounded parallelism'],
  files: ['Model files', 'GGUF inventory, saved launch profiles and deletion'],
}

const HISTORY_LENGTH = 120

export default function App() {
  const route = useHashRoute()
  const [state, setState] = useState<State | null>(null)
  const [connected, setConnected] = useState(false)
  const [draft, setDraft] = useState<LaunchDraft | null>(null)
  const [deleting, setDeleting] = useState<ModelFile | null>(null)
  const [dark, setDark] = useState(() => localStorage.getItem('ld-theme') !== 'light')
  const [history, setHistory] = useState<History>({ bandwidth: [], running: [], jobs: [] })

  const refresh = useCallback(async () => {
    try {
      const next = await api<State>('state')
      setState(next)
      setConnected(true)
      const push = (arr: number[], value: number) => [...arr, value].slice(-HISTORY_LENGTH)
      setHistory((h) => ({
        bandwidth: push(h.bandwidth, next.jobs.reduce((sum, job) => sum + job.bytes_per_second, 0)),
        running: push(h.running, next.runs.filter((r) => r.status === 'running' || r.status === 'stopping').length),
        jobs: push(h.jobs, next.jobs.filter((j) => j.status === 'queued' || j.status === 'downloading').length),
      }))
    } catch {
      setConnected(false)
    }
  }, [])

  useEffect(() => {
    refresh()
    const timer = setInterval(refresh, 2000)
    return () => clearInterval(timer)
  }, [refresh])

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    localStorage.setItem('ld-theme', dark ? 'dark' : 'light')
  }, [dark])

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
    [refresh]
  )

  const data = state ?? EMPTY_STATE
  const running = data.runs.filter((r) => r.status === 'running' || r.status === 'stopping')
  const detailRun: Run | undefined = route.view === 'server' ? data.runs.find((r) => r.id === route.id) : undefined

  const [title, subtitle] =
    route.view === 'server'
      ? detailRun
        ? [detailRun.launch.name, `${detailRun.launch.model} · 127.0.0.1:${detailRun.launch.port}`]
        : ['Server', 'Unknown run']
      : TITLES[route.view]

  return (
    <div className="flex min-h-dvh flex-col md:flex-row">
      <NavRail active={route.view} connected={connected} />

      <div className="flex min-w-0 grow flex-col">
        <header className="bg-background/70 sticky top-0 z-30 flex min-h-14 items-center gap-3.5 border-b px-4 backdrop-blur sm:px-6">
          <div className="min-w-0">
            <h1 className="truncate text-[15px] font-semibold tracking-tight">{title}</h1>
            <p className="text-muted-foreground mt-px hidden truncate text-xs sm:block">{subtitle}</p>
          </div>
          <span className="grow" />
          <span className="bg-secondary rounded-md border px-2 py-1 font-mono text-xs text-muted-foreground max-sm:hidden">
            /v1 · {running.length} {running.length === 1 ? 'model' : 'models'} live
          </span>
          <Button
            variant="ghost"
            size="icon"
            aria-label="Toggle theme"
            onClick={() => setDark(!dark)}
          >
            {dark ? <SunIcon className="size-4" /> : <MoonIcon className="size-4" />}
          </Button>
          <SettingsDialog state={state} connected={connected} />
          <Button onClick={() => setDraft({})} disabled={!state}>
            <PlayIcon className="size-4" />
            <span className="max-sm:hidden">Launch server</span>
            <span className="sm:hidden">Launch</span>
          </Button>
        </header>

        <main className="grow px-4 pt-5 pb-24 sm:px-6 md:pb-12">
          <div className="mx-auto grid w-full max-w-6xl content-start gap-3.5">
            {state ? (
              <>
                {route.view === 'dashboard' && <DashboardView state={data} history={history} />}
                {route.view === 'servers' && <ServersView runs={data.runs} />}
                {route.view === 'server' && (
                  <ServerDetail
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
              </>
            ) : (
              <p className="text-muted-foreground py-16 text-center text-sm">
                {connected ? 'Loading…' : 'Cannot reach the llama-desk API. Is the manager running?'}
              </p>
            )}

            <footer className="text-muted-foreground mt-4 flex flex-wrap gap-x-5 gap-y-1 font-mono text-xs">
              {state && (
                <>
                  <span>models {state.settings.models}</span>
                  <span>binary {state.settings.binary}</span>
                  <span>
                    {state.models.length} files · {bytes(data.models.reduce((s, m) => s + m.size, 0))}
                  </span>
                  <button className="underline-offset-2 hover:underline" onClick={() => navigate('files')}>
                    inventory
                  </button>
                </>
              )}
            </footer>
          </div>
        </main>
      </div>

      <Toaster theme={dark ? 'dark' : 'light'} />
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
            <AlertDialogTitle>Delete model?</AlertDialogTitle>
            <AlertDialogDescription className="font-mono text-xs break-all">
              {deleting ? `${deleting.path} · ${bytes(deleting.size)}` : ''}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() =>
                deleting &&
                call(`models?path=${encodeURIComponent(deleting.path)}`, 'DELETE', undefined, 'Deleted')
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
        <Button variant="ghost" size="icon" aria-label="Configuration">
          <InfoIcon className="size-4" />
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Configuration</DialogTitle>
        </DialogHeader>
        {state && (
          <div className="grid gap-2 text-xs">
            <SettingRow label="Models" value={state.settings.models} />
            <SettingRow label="llama-server" value={state.settings.binary} />
            <SettingRow label="Downloads" value={`${state.settings.parallel_downloads} parallel`} />
            <SettingRow label="API" value={connected ? 'connected' : 'unreachable'} />
            <Separator className="my-1" />
            <p className="text-muted-foreground">
              Gateway: <span className="font-mono">GET /v1/models</span> ·{' '}
              <span className="font-mono">POST /v1/chat/completions</span> routed by model name.
            </p>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function SettingRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right font-mono break-all">{value}</span>
    </div>
  )
}
