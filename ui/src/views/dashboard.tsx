import { useState } from 'react'
import { MeterRow } from '@/components/meter'
import { AreaChart, BarChart, Sparkline } from '@/components/sparkline'
import { ServerRow } from '@/views/servers'
import { Button } from '@/components/ui/button'
import { bytes } from '@/lib/format'
import { navigate } from '@/lib/router'
import type { State } from '@/lib/api'

export interface History {
  bandwidth: number[]
  running: number[]
  jobs: number[]
}

type Series = keyof History

const CHIPS: { id: Series; label: string }[] = [
  { id: 'bandwidth', label: 'MB/s' },
  { id: 'running', label: 'servers' },
  { id: 'jobs', label: 'jobs' },
]

const SEGMENT_COLORS = ['var(--t-accent)', 'var(--t-accent-ink)', 'var(--t-text-3)', 'var(--t-line)']

export function DashboardView({ state, history }: { state: State; history: History }) {
  const [series, setSeries] = useState<Series>('bandwidth')
  const running = state.runs.filter((r) => r.status === 'running' || r.status === 'stopping')
  const failed = state.runs.filter((r) => r.status === 'failed')
  const activeJobs = state.jobs.filter((j) => j.status === 'queued' || j.status === 'downloading')
  const downloading = state.jobs.filter((j) => j.status === 'downloading')
  const bandwidth = downloading.reduce((sum, j) => sum + j.bytes_per_second, 0)
  const disk = state.models.reduce((sum, m) => sum + m.size, 0)

  const values = history[series]
  const peak = values.length ? Math.max(...values) : 0
  const peakLabel =
    series === 'bandwidth'
      ? `peak ${bytes(peak)}/s`
      : series === 'running'
        ? `peak ${peak} servers`
        : `peak ${peak} jobs`

  return (
    <div className="grid gap-5">
      <div className="stat-grid">
        <Stat
          k="Active servers"
          v={`${running.length}`}
          u={`/ ${state.runs.length} managed`}
          f={
            failed.length
              ? `${running.length} live · ${failed.length} failed`
              : running.length
                ? `${running.length} healthy`
                : 'no managed processes'
          }
          fClass={failed.length ? 'is-bad' : running.length ? 'is-ok' : undefined}
        />
        <Stat
          k="Aliases on /v1"
          v={`${running.length}`}
          f={running.length ? running.map((r) => r.launch.name).join(' · ') : 'nothing serving yet'}
        />
        <Stat
          k="Downloads"
          v={`${downloading.length}`}
          u={`/ ${state.settings.parallel_downloads} slots`}
          f={bandwidth > 0 ? `≈ ${bytes(bandwidth)}/s now` : 'idle'}
          aside={
            <span className="block h-7 w-[110px] shrink-0">
              <Sparkline values={history.bandwidth} />
            </span>
          }
        />
        <Stat
          k="Model disk"
          v={`${state.catalog.length}`}
          u={`· ${bytes(disk)}`}
          f={`${state.models.length} GGUF files in ${state.settings.models}`}
        />
      </div>

      <div className="grid gap-5 lg:grid-cols-3">
        <section className="panel lg:col-span-2">
          <div className="panel-head">
            <span className="panel-title">Activity</span>
            <span className="grow" />
            <span className="seg">
              {CHIPS.map((chip) => (
                <button key={chip.id} onClick={() => setSeries(chip.id)} aria-pressed={series === chip.id}>
                  {chip.label}
                </button>
              ))}
            </span>
          </div>
          <div className="panel-body">
            {series === 'jobs' ? (
              <BarChart values={history.jobs} height={190} />
            ) : (
              <AreaChart values={series === 'running' ? history.running : history.bandwidth} height={190} />
            )}
            <div className="num mt-2.5 flex justify-end text-xs text-[var(--t-text-3)]">
              {values.length > 1 ? peakLabel : 'collecting samples…'}
            </div>
          </div>
        </section>

        <section className="panel">
          <div className="panel-head">
            <span className="panel-title">Model storage</span>
            <span className="panel-meta">
              {bytes(disk)} · {state.models.length} files
            </span>
          </div>
          <div className="panel-body">
            {disk > 0 ? (
              <div className="grid gap-3.5">
                <div className="progress stacked" aria-hidden="true">
                  {diskSegments(state).map((s) => (
                    <i
                      key={s.label}
                      style={{ width: `${(s.value / disk) * 100}%`, background: s.color }}
                      title={`${s.label} — ${bytes(s.value)}`}
                    />
                  ))}
                </div>
                <div className="grid gap-2 text-xs">
                  {diskSegments(state).map((s) => (
                    <div key={s.label} className="flex items-center gap-2 text-[var(--t-text-2)]">
                      <span className="size-2 shrink-0" style={{ background: s.color }} />
                      <span className="truncate">{s.label}</span>
                      <span className="num ml-auto shrink-0 text-[var(--t-text)]">{bytes(s.value)}</span>
                      <span className="num w-10 shrink-0 text-right text-[var(--t-text-3)]">
                        {((s.value / disk) * 100).toFixed(0)}%
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            ) : (
              <p className="py-6 text-center text-sm text-[var(--t-text-2)]">No models on disk yet.</p>
            )}
          </div>
        </section>
      </div>

      <section className="panel">
        <div className="panel-head">
          <span className="panel-title">Manager</span>
          <span className="panel-meta">capacity in use</span>
        </div>
        <div className="panel-body grid gap-x-8 md:grid-cols-3">
          <MeterRow
            label="Servers"
            value={state.runs.length ? (running.length / state.runs.length) * 100 : 0}
            detail={`${running.length} / ${state.runs.length}`}
            tone="ok"
          />
          <MeterRow
            label="Downloads"
            value={
              state.settings.parallel_downloads ? (downloading.length / state.settings.parallel_downloads) * 100 : 0
            }
            detail={`${downloading.length} / ${state.settings.parallel_downloads}`}
            tone="info"
          />
          <MeterRow
            label="Jobs open"
            value={state.jobs.length ? (activeJobs.length / state.jobs.length) * 100 : 0}
            detail={`${activeJobs.length} / ${state.jobs.length}`}
            tone="warn"
          />
        </div>
      </section>

      <div>
        <div className="sub-label mb-3.5">Servers on this host</div>
        <div className="grid gap-2.5">
          {running.length ? (
            running.map((run) => <ServerRow key={run.id} run={run} />)
          ) : (
            <p className="border border-dashed border-[var(--t-line)] py-8 text-center text-sm text-[var(--t-text-2)]">
              No servers serving right now.
            </p>
          )}
        </div>
        <div className="mt-3.5 flex justify-end">
          <Button variant="secondary" size="sm" onClick={() => navigate('servers')}>
            All servers →
          </Button>
        </div>
      </div>
    </div>
  )
}

interface DiskSegment {
  label: string
  value: number
  color: string
}

function diskSegments(state: State): DiskSegment[] {
  const byRepo = new Map<string, number>()
  for (const model of state.catalog) {
    const label = model.repo || '(root)'
    byRepo.set(label, (byRepo.get(label) || 0) + model.size)
  }
  const sorted = [...byRepo.entries()].sort((a, b) => b[1] - a[1])
  const top = sorted.slice(0, 4)
  const rest = sorted.slice(4)
  const segments = top.map(([label, value], i) => ({ label, value, color: SEGMENT_COLORS[i] }))
  if (rest.length)
    segments.push({
      label: `${rest.length} other`,
      value: rest.reduce((sum, [, v]) => sum + v, 0),
      color: 'var(--t-text-3)',
    })
  return segments
}

function Stat({
  k,
  v,
  u,
  f,
  fClass,
  aside,
}: {
  k: string
  v: string
  u?: string
  f?: string
  fClass?: string
  aside?: React.ReactNode
}) {
  return (
    <div className="stat">
      <div className="k">{k}</div>
      <div className="v">
        {v}
        {u && <span className="u">{u}</span>}
      </div>
      {(f || aside) && (
        <div className="f-row">
          <span className={`f ${fClass ?? ''}`}>{f}</span>
          {aside}
        </div>
      )}
    </div>
  )
}
