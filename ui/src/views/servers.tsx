import { ChevronRightIcon } from '@/components/icons'
import { StatusPill } from '@/components/status-pill'
import { type CacheView, type Run } from '@/lib/api'
import { uptime } from '@/lib/format'
import { navigate } from '@/lib/router'

export function CacheBadge({ cache }: { cache?: CacheView }) {
  if (!cache?.requested) return null
  if (cache.status === 'ready')
    return <span className="badge shrink-0">cache</span>
  if (cache.status === 'checking')
    return <span className="badge badge-info shrink-0">cache…</span>
  return (
    <span className="badge badge-warn shrink-0" title={cache.reason}>
      {cache.status === 'error' ? 'cache error' : 'no cache'}
    </span>
  )
}

function runRank(run: Run): number {
  return run.status === 'running' ? 0 : run.status === 'stopping' ? 1 : 2
}

function sortedRuns(runs: Run[]): Run[] {
  return [...runs].sort((a, b) => runRank(a) - runRank(b) || +new Date(b.started) - +new Date(a.started))
}

export function ServerRow({ run }: { run: Run }) {
  return (
    <button onClick={() => navigate(`server/${run.id}`)} className="row-btn">
      <span className="flex items-center gap-3.5">
        <StatusPill status={run.status} />
        <span className="min-w-0 grow">
          <span className="flex items-center gap-2 text-sm font-semibold">
            <span className="mono truncate">{run.launch.name}</span>
            <span className="num rounded-[var(--t-r-1)] border border-[var(--t-line)] bg-[var(--t-bg-deep)] px-1.5 py-0.5 text-xs text-[var(--t-text-2)]">
              :{run.launch.port}
            </span>
            <CacheBadge cache={run.cache} />
          </span>
          <span className="mono mt-0.5 flex flex-wrap gap-x-3.5 gap-y-0.5 text-xs text-[var(--t-text-3)]">
            <span className="truncate">{run.launch.model}</span>
            <span className="shrink-0">ctx {run.launch.context.toLocaleString()}</span>
            {run.pid ? <span className="shrink-0">pid {run.pid}</span> : null}
          </span>
        </span>
        <span className="num shrink-0 text-right text-xs text-[var(--t-text-3)]">
          {uptime(run.started, run.ended)}
        </span>
        <ChevronRightIcon className="size-4 shrink-0 text-[var(--t-text-3)]" />
      </span>
    </button>
  )
}

export function ServersView({ runs }: { runs: Run[] }) {
  return (
    <div>
      <h2 className="pane-title">Servers</h2>
      <p className="pane-lede">Your binary, your arguments, exactly as launched.</p>
      <div className="grid gap-2.5">
        {sortedRuns(runs).map((run) => (
          <ServerRow key={run.id} run={run} />
        ))}
        {!runs.length && (
          <p className="border border-dashed border-[var(--t-line)] py-10 text-center text-sm text-[var(--t-text-2)]">
            Nothing managed yet. Pick a model and launch a server.
          </p>
        )}
      </div>
    </div>
  )
}
