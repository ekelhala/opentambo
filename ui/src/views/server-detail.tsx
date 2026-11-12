import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { StatusPill } from '@/components/status-pill'
import { api, type Run } from '@/lib/api'
import { date, uptime } from '@/lib/format'
import { navigate } from '@/lib/router'
import {
  AlertTriangleIcon,
  ArrowLeftIcon,
  CopyIcon,
  PlayIcon,
  SquareIcon,
} from 'lucide-react'
import { toast } from 'sonner'

export function ServerDetail({
  run,
  onChanged,
  onRelaunch,
}: {
  run: Run | undefined
  onChanged: () => void
  onRelaunch: (run: Run) => void
}) {
  const runId = run?.id
  const [logs, setLogs] = useState('')
  const [health, setHealth] = useState('')
  const logRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    setLogs('')
    setHealth('')
    if (!runId) return
    let alive = true
    const load = async () => {
      try {
        const data = await api<{ logs: string }>(`runs/${runId}/logs`)
        if (!alive) return
        setLogs(data.logs)
        requestAnimationFrame(() => {
          const viewport = logRef.current
          if (viewport) viewport.scrollTop = viewport.scrollHeight
        })
      } catch {
        /* the run may have been cleaned up between polls */
      }
    }
    load()
    const timer = setInterval(load, 2000)
    return () => {
      alive = false
      clearInterval(timer)
    }
  }, [runId])

  if (!run) {
    return (
      <div className="grid gap-4">
        <Button variant="outline" size="sm" className="w-fit" onClick={() => navigate('servers')}>
          <ArrowLeftIcon className="size-3.5" /> Servers
        </Button>
        <p className="text-muted-foreground py-10 text-center text-sm">
          This run is no longer in state.
        </p>
      </div>
    )
  }

  async function act(action: 'stop' | 'health') {
    if (!run) return
    try {
      if (action === 'stop') {
        await api(`runs/${run.id}/stop`, 'POST', {})
        onChanged()
      } else {
        const data = await api<{ status: string; detail?: string; http_status?: number; response?: unknown }>(
          `runs/${run.id}/health`
        )
        setHealth(
          data.http_status
            ? `${data.http_status} ${JSON.stringify(data.response ?? '')}`
            : data.detail
              ? `${data.status}: ${data.detail}`
              : data.status
        )
      }
    } catch (error) {
      setHealth(error instanceof Error ? error.message : String(error))
    }
  }

  const serving = run.status === 'running' || run.status === 'stopping'
  const command = run.command.join(' ')

  return (
    <div className="grid gap-3.5">
      <div className="flex flex-wrap items-center gap-2.5">
        <Button variant="outline" size="sm" onClick={() => navigate('servers')}>
          <ArrowLeftIcon className="size-3.5" /> Servers
        </Button>
        <span className="grow" />
        {serving ? (
          <>
            <Button
              variant="outline"
              size="sm"
              className="text-destructive"
              disabled={run.status === 'stopping'}
              onClick={() => act('stop')}
            >
              <SquareIcon className="size-3" />
              {run.status === 'stopping' ? 'Stopping…' : 'Stop'}
            </Button>
            <Button variant="outline" size="sm" onClick={() => act('health')}>
              Health
            </Button>
          </>
        ) : (
          <Button size="sm" onClick={() => onRelaunch(run)}>
            <PlayIcon className="size-3" /> Run again
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            navigator.clipboard.writeText(command)
            toast.success('Command copied')
          }}
        >
          <CopyIcon className="size-3.5" /> Copy command
        </Button>
      </div>
      {health && <p className="text-muted-foreground font-mono text-xs">{health}</p>}

      {run.error && (
        <div className="border-destructive/40 bg-destructive/10 flex items-start gap-2.5 rounded-xl border p-3.5 text-[13px] text-destructive">
          <AlertTriangleIcon className="mt-0.5 size-4 shrink-0" />
          <span className="break-all whitespace-pre-wrap">{run.error}</span>
        </div>
      )}

      <div className="grid grid-cols-2 gap-3.5 lg:grid-cols-4">
        <DetailStat label="Status">
          <div className="mt-2.5">
            <StatusPill status={run.status} />
          </div>
          <p className="text-muted-foreground mt-2 text-xs">
            {serving ? 'managed process' : run.status === 'failed' ? 'process exited with error' : 'process not serving'}
          </p>
        </DetailStat>
        <DetailStat label="Process" value={run.pid ? String(run.pid) : '—'}>
          <p className="text-muted-foreground mt-2 text-xs tabular-nums">
            {uptime(run.started, run.ended)}{run.ended ? ' · ended' : ''}
          </p>
        </DetailStat>
        <DetailStat label="Endpoint" value={`:${run.launch.port}`}>
          <p className="text-muted-foreground mt-2 text-xs">
            127.0.0.1 · alias <span className="font-mono">{run.launch.name}</span>
          </p>
        </DetailStat>
        <DetailStat label="Launch" value={`${run.launch.context.toLocaleString()} ctx`}>
          <p className="text-muted-foreground mt-2 text-xs tabular-nums">
            {run.launch.gpu_layers} GPU layers · started {date(run.started)}
          </p>
        </DetailStat>
      </div>

      <div className="grid gap-3.5 lg:grid-cols-3">
        <Card className="lg:col-span-1">
          <CardHeader>
            <CardTitle>Launch configuration</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3 text-xs">
            <div className="grid grid-cols-[96px_1fr] gap-x-3 gap-y-1">
              <span className="text-muted-foreground">Model</span>
              <span className="font-mono break-all">{run.launch.model}</span>
              <span className="text-muted-foreground">Endpoint</span>
              <span className="font-mono break-all">http://127.0.0.1:{run.launch.port}</span>
              <span className="text-muted-foreground">Started</span>
              <span className="tabular-nums">{date(run.started)}</span>
            </div>
            <ArgBlock label="Arguments" value={run.launch.args.length ? run.launch.args.join('\n') : '(none)'} />
            <ArgBlock
              label="Environment overrides"
              value={
                Object.keys(run.launch.env).length
                  ? Object.entries(run.launch.env).map(([k, v]) => `${k}=${v}`).join('\n')
                  : '(none)'
              }
            />
            <ArgBlock label="Full command" value={command} wrap />
          </CardContent>
        </Card>
        <Card className="lg:col-span-2">
          <CardHeader className="flex-row items-center">
            <CardTitle>Live log</CardTitle>
            <span className="text-muted-foreground ml-auto text-xs font-normal normal-case tracking-normal">
              last 128 KiB
            </span>
          </CardHeader>
          <CardContent>
            <div
              ref={logRef}
              className="bg-zinc-950 h-[380px] overflow-y-auto rounded-lg border border-zinc-800 p-3.5"
            >
              <pre className="font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-zinc-300">
                {logs || 'Waiting for output…'}
              </pre>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function DetailStat({
  label,
  value,
  children,
}: {
  label: string
  value?: string
  children?: React.ReactNode
}) {
  return (
    <Card className="py-4">
      <CardContent className="px-4">
        <div className="text-muted-foreground text-[11px] font-medium tracking-wider uppercase">
          {label}
        </div>
        {value !== undefined && (
          <div className="mt-1.5 truncate text-lg font-semibold tabular-nums">{value}</div>
        )}
        {children}
      </CardContent>
    </Card>
  )
}

function ArgBlock({ label, value, wrap }: { label: string; value: string; wrap?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-muted-foreground">{label}</span>
      <pre
        className={`bg-muted overflow-x-auto rounded-md p-2.5 font-mono text-xs leading-relaxed ${
          wrap ? 'break-all whitespace-pre-wrap' : 'whitespace-pre'
        }`}
      >
        {value}
      </pre>
    </div>
  )
}
