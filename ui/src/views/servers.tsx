import { ChevronRightIcon } from '@/components/icons'
import { CacheBadge } from '@/components/cache-status'
import { StatusPill } from '@/components/status-pill'
import { type Run } from '@/lib/api'
import { uptime } from '@/lib/format'
import { navigate } from '@/lib/router'

function runRank(run: Run): number {
  return run.status === 'running' ? 0 : run.status === 'stopping' ? 1 : 2
}

function sortedRuns(runs: Run[]): Run[] {
  return [...runs].sort((a, b) => runRank(a) - runRank(b) || +new Date(b.started) - +new Date(a.started))
}

export function ServerRow({ run }: { run: Run }) {
  return (
    <button onClick={() => navigate(`server/${run.id}`)} className="row-btn">
      <span className="flex w-full items-center gap-3.5">
        <StatusPill status={run.status} className="shrink-0" />
        <span className="min-w-0 grow">
          <span className="flex min-w-0 items-center gap-2 text-sm font-semibold">
            <span className="mono min-w-0 truncate">{run.launch.name}</span>
            <span className="num max-sm:hidden rounded-[var(--t-r-1)] border border-[var(--t-line)] bg-[var(--t-bg-deep)] px-1.5 py-0.5 text-xs text-[var(--t-text-2)]">
              :{run.launch.port}
            </span>
            <span className="max-sm:hidden">
              <CacheBadge cache={run.cache} />
            </span>
          </span>
          <span className="mono mt-0.5 flex flex-wrap gap-x-3.5 gap-y-0.5 text-xs text-[var(--t-text-3)]">
            <span className="truncate">{run.launch.model}</span>
            <span className="shrink-0">ctx {run.launch.context.toLocaleString()}</span>
            {run.pid ? <span className="shrink-0">pid {run.pid}</span> : null}
          </span>
        </span>
        <span className="num max-sm:hidden shrink-0 text-right text-xs text-[var(--t-text-3)]">
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
      <div className="grid grid-cols-[minmax(0,1fr)] gap-2.5">
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
