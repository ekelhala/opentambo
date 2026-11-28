import { useState } from 'react'
import { BackIcon, CopyIcon, PlayIcon, PulseIcon, StopIcon, WarnIcon } from '@/components/icons'
import { CachePanel } from '@/components/cache-status'
import { LiveLog } from '@/components/live-log'
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
  const [health, setHealth] = useState('')
  const [tab, setTab] = useState<'log' | 'config'>('log')

  if (!run) {
    return (
      <div className="grid grid-cols-[minmax(0,1fr)] gap-4">
        <Button variant="secondary" size="sm" className="w-fit" onClick={() => navigate('servers')}>
          <BackIcon /> Servers
        </Button>
        <p className="py-10 text-center text-sm text-[var(--t-text-2)]">This server is no longer running.</p>
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
          `runs/${run.id}/health`,
        )
        setHealth(
          data.http_status
            ? `${data.http_status} ${JSON.stringify(data.response ?? '')}`
            : data.detail
              ? `${data.status}: ${data.detail}`
              : data.status,
        )
      }
    } catch (error) {
      setHealth(error instanceof Error ? error.message : String(error))
    }
  }

  const serving = run.status === 'running' || run.status === 'stopping'
  const command = run.command.join(' ')

  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-3.5">
      <div className="srv">
        <div className="srv-head">
          <Button variant="secondary" size="sm" onClick={() => navigate('servers')}>
            <BackIcon /> Servers
          </Button>
          <StatusPill status={run.status} />
          <span className="flex min-w-0 items-baseline gap-2">
            <span className="srv-name truncate">{run.launch.name}</span>
            <span className="srv-meta shrink-0">:{run.launch.port}</span>
          </span>
          <span className="grow" />
          {serving ? (
            <>
              <Button variant="danger" size="sm" disabled={run.status === 'stopping'} onClick={() => act('stop')}>
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
            aria-label="Copy command"
            onClick={() => {
              navigator.clipboard.writeText(command)
              toast.success('Command copied')
            }}
          >
            <CopyIcon />
          </Button>
        </div>
        <div className="meta-strip">
          <Meta k="uptime" v={uptime(run.started, run.ended)} />
          {run.pid ? <Meta k="pid" v={String(run.pid)} /> : null}
          <Meta k="ctx" v={run.launch.context.toLocaleString()} />
          <Meta k="gpu layers" v={String(run.launch.gpu_layers)} />
          <Meta k="started" v={date(run.started)} />
          {run.ended ? <Meta k="ended" v={date(run.ended)} /> : null}
        </div>
        {health && <p className="mono mt-2.5 text-xs break-all text-[var(--t-text-2)]">{health}</p>}
      </div>

      {run.error && (
        <div className="callout callout-bad">
          <WarnIcon />
          <p className="break-words whitespace-pre-wrap">
            <strong>Something went wrong with this process.</strong>
            <br />
            {run.error}
          </p>
        </div>
      )}

      {run.cache?.requested && <CachePanel run={run} />}

      {tab === 'log' ? (
        <LiveLog key={run.id} runId={run.id} leading={<RunTabs tab={tab} onSelect={setTab} />} />
      ) : (
        <div className="panel">
          <div className="panel-head">
            <RunTabs tab={tab} onSelect={setTab} />
          </div>
          <div className="panel-body grid grid-cols-[minmax(0,1fr)] gap-3 text-xs">
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
                  ? Object.entries(run.launch.env || {})
                      .map(([k, v]) => `${k}=${v}`)
                      .join('\n')
                  : '(none)'
              }
            />
            <ArgBlock label="Full command" value={command} wrap />
          </div>
        </div>
      )}
    </div>
  )
}

function RunTabs({ tab, onSelect }: { tab: 'log' | 'config'; onSelect: (tab: 'log' | 'config') => void }) {
  return (
    <span className="tabs" role="tablist" aria-label="Run details">
      <button role="tab" aria-selected={tab === 'log'} onClick={() => onSelect('log')}>
        Live log
      </button>
      <button role="tab" aria-selected={tab === 'config'} onClick={() => onSelect('config')}>
        Configuration
      </button>
    </span>
  )
}

function Meta({ k, v }: { k: string; v: string }) {
  return (
    <span className="flex min-w-0 items-baseline gap-2">
      <span className="shrink-0 text-[var(--t-text-3)]">{k}</span>
      <span className="num truncate text-[var(--t-text)]">{v}</span>
    </span>
  )
}

function ArgBlock({ label, value, wrap }: { label: string; value: string; wrap?: boolean }) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)] gap-1">
      <span className="text-[var(--t-text-3)]">{label}</span>
      <pre className={wrap ? 'well wrap' : 'well nowrap'}>{value}</pre>
    </div>
  )
}
