import { StatusPill } from '@/components/status-pill'
import { type Run } from '@/lib/api'
import { uptime } from '@/lib/format'
import { navigate } from '@/lib/router'
import { ChevronRightIcon } from 'lucide-react'

function runRank(run: Run): number {
  return run.status === 'running' ? 0 : run.status === 'stopping' ? 1 : 2
}

function sortedRuns(runs: Run[]): Run[] {
  return [...runs].sort((a, b) => runRank(a) - runRank(b) || +new Date(b.started) - +new Date(a.started))
}

export function ServerRow({ run }: { run: Run }) {
  return (
    <button
      onClick={() => navigate(`server/${run.id}`)}
      className="bg-card hover:bg-accent/50 flex w-full items-center gap-3.5 rounded-xl border px-4 py-3 text-left transition-colors"
    >
      <StatusPill status={run.status} />
      <span className="min-w-0 grow">
        <span className="flex items-center gap-2 text-sm font-medium">
          <span className="truncate font-mono">{run.launch.name}</span>
          <span className="bg-secondary rounded-md px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
            :{run.launch.port}
          </span>
        </span>
        <span className="text-muted-foreground mt-0.5 flex flex-wrap gap-x-3.5 gap-y-0.5 font-mono text-xs">
          <span className="truncate">{run.launch.model}</span>
          <span className="shrink-0">ctx {run.launch.context.toLocaleString()}</span>
          {run.pid ? <span className="shrink-0">pid {run.pid}</span> : null}
        </span>
      </span>
      <span className="text-muted-foreground shrink-0 text-right text-xs tabular-nums">
        {uptime(run.started, run.ended)}
      </span>
      <ChevronRightIcon className="text-muted-foreground size-4 shrink-0" />
    </button>
  )
}

export function ServersView({ runs }: { runs: Run[] }) {
  return (
    <div className="grid gap-2.5">
      {sortedRuns(runs).map((run) => (
        <ServerRow key={run.id} run={run} />
      ))}
      {!runs.length && (
        <p className="text-muted-foreground py-10 text-center text-sm">
          Nothing managed yet. Pick a model and launch a server.
        </p>
      )}
    </div>
  )
}
