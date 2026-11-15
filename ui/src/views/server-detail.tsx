import { useEffect, useRef, useState } from 'react'
import { BackIcon, CopyIcon, PlayIcon, PulseIcon, StopIcon, WarnIcon } from '@/components/icons'
import { CachePanel } from '@/components/cache-status'
import { Button } from '@/components/ui/button'
import { StatusPill } from '@/components/status-pill'
import { api, type Run } from '@/lib/api'
import { date, uptime } from '@/lib/format'
import { navigate } from '@/lib/router'
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

  // the consumer keys this component by run id, so log/health state starts fresh per run
  useEffect(() => {
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
        <Button variant="secondary" size="sm" className="w-fit" onClick={() => navigate('servers')}>
          <BackIcon /> Servers
        </Button>
        <p className="py-10 text-center text-sm text-[var(--t-text-2)]">
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
      <div className="srv">
        <div className="srv-head">
          <Button variant="secondary" size="sm" onClick={() => navigate('servers')}>
            <BackIcon /> Servers
          </Button>
          <StatusPill status={run.status} />
          <span className="srv-name truncate">{run.launch.name}</span>
          <span className="srv-meta">:{run.launch.port}</span>
          <span className="grow" />
          {serving ? (
            <>
              <Button
                variant="danger"
                size="sm"
                disabled={run.status === 'stopping'}
                onClick={() => act('stop')}
              >
                <StopIcon />
                {run.status === 'stopping' ? 'Stopping…' : 'Stop'}
              </Button>
              <Button variant="secondary" size="sm" onClick={() => act('health')}>
                <PulseIcon /> Health
              </Button>
            </>
          ) : (
            <Button size="sm" onClick={() => onRelaunch(run)}>
              <PlayIcon /> Run again
            </Button>
          )}
          <Button
            variant="secondary"
            size="sm"
            onClick={() => {
              navigator.clipboard.writeText(command)
              toast.success('Command copied')
            }}
          >
            <CopyIcon /> Copy command
          </Button>
        </div>
        {health && <p className="mono mt-2.5 text-xs break-all text-[var(--t-text-2)]">{health}</p>}
      </div>

      {run.error && (
        <div className="callout callout-bad">
          <WarnIcon />
          <p className="break-all whitespace-pre-wrap">
            <strong>The process reported an error.</strong>
            <br />
            {run.error}
          </p>
        </div>
      )}

      <div className="stat-grid">
        <div className="stat">
          <span className="k">Status</span>
          <div className="mt-2.5">
            <StatusPill status={run.status} />
          </div>
          <span className="f">{serving ? 'managed process' : run.status === 'failed' ? 'exited with error' : 'not serving'}</span>
        </div>
        <div className="stat">
          <span className="k">Process</span>
          <span className="v">
            {run.pid ?? '—'}
            <span className="u">pid</span>
          </span>
          <span className="f">
            {uptime(run.started, run.ended)}
            {run.ended ? ' · ended' : ''}
          </span>
        </div>
        <div className="stat">
          <span className="k">Endpoint</span>
          <span className="v">:{run.launch.port}</span>
          <span className="f">127.0.0.1 · alias {run.launch.name}</span>
        </div>
        <div className="stat">
          <span className="k">Launch</span>
          <span className="v">
            {run.launch.context.toLocaleString()}
            <span className="u">ctx</span>
          </span>
          <span className="f">
            {run.launch.gpu_layers} GPU layers · started {date(run.started)}
          </span>
        </div>
      </div>

      {run.cache?.requested && <CachePanel run={run} />}

      <div className="grid gap-3.5 lg:grid-cols-3">
        <div className="panel lg:col-span-1">
          <div className="panel-head">
            <span className="panel-title">Launch configuration</span>
          </div>
          <div className="panel-body grid gap-3 text-xs">
            <div className="grid grid-cols-[96px_1fr] gap-x-3 gap-y-1">
              <span className="text-[var(--t-text-3)]">Model</span>
              <span className="mono break-all">{run.launch.model}</span>
              <span className="text-[var(--t-text-3)]">Endpoint</span>
              <span className="mono break-all">http://127.0.0.1:{run.launch.port}</span>
              <span className="text-[var(--t-text-3)]">Started</span>
              <span className="num">{date(run.started)}</span>
            </div>
            <ArgBlock label="Arguments" value={run.launch.args.length ? run.launch.args.join('\n') : '(none)'} />
            <ArgBlock
              label="Environment overrides"
              value={
                Object.entries(run.launch.env || {}).length
                  ? Object.entries(run.launch.env || {}).map(([k, v]) => `${k}=${v}`).join('\n')
                  : '(none)'
              }
            />
            <ArgBlock label="Full command" value={command} wrap />
          </div>
        </div>
        <div className="panel lg:col-span-2">
          <div className="panel-head">
            <span className="panel-title">Live log</span>
            <span className="panel-meta">last 128 KiB · polls every 2 s</span>
          </div>
          <div className="panel-body">
            <div ref={logRef} className="console h-[380px]">
              <pre className="break-all whitespace-pre-wrap">{logs || 'Waiting for output…'}</pre>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

function ArgBlock({ label, value, wrap }: { label: string; value: string; wrap?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-[var(--t-text-3)]">{label}</span>
      <pre className={wrap ? 'well wrap' : 'well nowrap'}>{value}</pre>
    </div>
  )
}
