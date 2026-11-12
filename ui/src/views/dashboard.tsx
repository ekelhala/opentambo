import { useState } from 'react'
import { Donut, type DonutSegment } from '@/components/donut'
import { MeterRow } from '@/components/meter'
import { AreaChart, BarChart, Sparkline } from '@/components/sparkline'
import { ServerRow } from '@/views/servers'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
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

const SEGMENT_COLORS = [
  'var(--color-sky-500)',
  'var(--color-emerald-500)',
  'var(--color-violet-400)',
  'var(--color-amber-500)',
  'var(--color-zinc-500)',
]

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
    <div className="grid gap-3.5">
      <div className="grid grid-cols-2 gap-3.5 lg:grid-cols-4">
        <Stat
          label="Active servers"
          value={`${running.length}`}
          unit={`/ ${state.runs.length} managed`}
          foot={
            failed.length
              ? `● ${running.length} live · ${failed.length} failed`
              : running.length
                ? `● ${running.length} healthy`
                : 'no managed processes'
          }
          footClassName={failed.length ? 'text-destructive' : undefined}
        />
        <Stat
          label="Aliases on /v1"
          value={`${running.length}`}
          foot={running.length ? running.map((r) => r.launch.name).join(' · ') : 'nothing serving yet'}
          footMono
        />
        <Stat
          label="Downloads"
          value={`${downloading.length}`}
          unit={`/ ${state.settings.parallel_downloads} slots`}
          foot={bandwidth > 0 ? `≈ ${bytes(bandwidth)}/s now` : 'idle'}
          spark={
            <span className="block h-7 w-[110px] shrink-0">
              <Sparkline values={history.bandwidth} className="text-sky-500" />
            </span>
          }
        />
        <Stat
          label="Model disk"
          value={`${state.models.length}`}
          unit={`· ${bytes(disk)}`}
          foot={state.settings.models}
          footMono
        />
      </div>

      <div className="grid gap-3.5 lg:grid-cols-3">
        <Card>
          <CardHeader className="flex-row items-center">
            <CardTitle>Activity</CardTitle>
            <span className="ml-auto flex gap-0.5 rounded-lg border bg-secondary p-0.5">
              {CHIPS.map((chip) => (
                <button
                  key={chip.id}
                  onClick={() => setSeries(chip.id)}
                  aria-pressed={series === chip.id}
                  className={`rounded-md px-3 py-1 text-xs font-medium transition-colors ${
                    series === chip.id
                      ? 'bg-background text-foreground shadow-sm'
                      : 'text-muted-foreground hover:text-foreground'
                  }`}
                >
                  {chip.label}
                </button>
              ))}
            </span>
          </CardHeader>
          <CardContent>
            {series === 'jobs' ? (
              <BarChart values={history.jobs} height={190} />
            ) : series === 'running' ? (
              <AreaChart values={history.running} height={190} className="text-emerald-500" />
            ) : (
              <AreaChart values={history.bandwidth} height={190} className="text-sky-500" />
            )}
            <div className="text-muted-foreground mt-2.5 flex justify-end font-mono text-xs">
              {values.length > 1 ? peakLabel : 'collecting samples…'}
            </div>
          </CardContent>
        </Card>
        <div className="grid content-start gap-3.5">
          <Card>
            <CardHeader className="flex-row items-center">
              <CardTitle>Model storage</CardTitle>
              <span className="text-muted-foreground ml-auto text-xs font-normal normal-case tracking-normal">
                {state.models.length} files
              </span>
            </CardHeader>
            <CardContent>
              {disk > 0 ? (
                <div className="flex flex-col items-center gap-4 sm:flex-row sm:items-center lg:flex-col xl:flex-row">
                  <Donut
                    segments={diskSegments(state)}
                    centerValue={bytes(disk).replace(/ \w+$/, '')}
                    centerLabel={bytes(disk).split(' ')[1]}
                    size={124}
                  />
                  <div className="grid w-full gap-2 text-xs">
                    {diskSegments(state).map((s) => (
                      <div key={s.label} className="text-muted-foreground flex items-center gap-2">
                        <span className="size-2 shrink-0 rounded-sm" style={{ background: s.color }} />
                        <span className="truncate">{s.label}</span>
                        <span className="text-foreground ml-auto shrink-0 tabular-nums">{bytes(s.value)}</span>
                      </div>
                    ))}
                  </div>
                </div>
              ) : (
                <p className="text-muted-foreground py-6 text-center text-sm">No models on disk yet.</p>
              )}
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>Manager</CardTitle>
            </CardHeader>
            <CardContent>
              <MeterRow
                label="Servers"
                value={state.runs.length ? (running.length / state.runs.length) * 100 : 0}
                detail={`${running.length} / ${state.runs.length}`}
              />
              <MeterRow
                label="Downloads"
                value={
                  state.settings.parallel_downloads
                    ? (downloading.length / state.settings.parallel_downloads) * 100
                    : 0
                }
                detail={`${downloading.length} / ${state.settings.parallel_downloads}`}
                className="bg-sky-500"
              />
              <MeterRow
                label="Jobs open"
                value={state.jobs.length ? (activeJobs.length / state.jobs.length) * 100 : 0}
                detail={`${activeJobs.length} / ${state.jobs.length}`}
                className="bg-amber-500"
              />
            </CardContent>
          </Card>
        </div>
      </div>

      <div className="grid gap-3.5 lg:grid-cols-3">
        <div className="grid content-start gap-2.5 lg:col-span-2">
          <div className="flex items-center gap-2">
            <h2 className="text-muted-foreground text-[11px] font-semibold tracking-wider uppercase">
              Active servers
            </h2>
            <span className="grow" />
            <Button variant="outline" size="sm" onClick={() => navigate('servers')}>
              Inspect →
            </Button>
          </div>
          {running.length ? (
            running.map((run) => <ServerRow key={run.id} run={run} />)
          ) : (
            <p className="text-muted-foreground rounded-xl border border-dashed py-8 text-center text-sm">
              No servers serving right now.
            </p>
          )}
        </div>
        <Card>
          <CardHeader>
            <CardTitle>Gateway</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-muted-foreground mb-2.5 text-[12.5px]">
              One OpenAI-compatible entry point. LiteLLM keeps owning keys and policies; aliases stay
              stable across ports.
            </p>
            <pre className="bg-muted overflow-x-auto rounded-md p-2.5 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
              {`POST /v1/chat/completions\n` +
                (running.length
                  ? running.map((r) => `  "model": "${r.launch.name}"  → 127.0.0.1:${r.launch.port}`).join('\n')
                  : '  (no aliases live)')}
            </pre>
            <div className="mt-3 flex flex-wrap gap-2">
              <Badge variant="secondary" className="bg-emerald-500/15 text-emerald-600 dark:text-emerald-400">
                GET /v1/models
              </Badge>
              <Badge variant="outline">chat · completions</Badge>
              <Badge variant="outline">embeddings</Badge>
              <Badge variant="outline">responses</Badge>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}

function diskSegments(state: State): DonutSegment[] {
  const byDir = new Map<string, number>()
  for (const model of state.models) {
    const dir = model.path.includes('/') ? model.path.split('/')[0] : '(root)'
    byDir.set(dir, (byDir.get(dir) || 0) + model.size)
  }
  const sorted = [...byDir.entries()].sort((a, b) => b[1] - a[1])
  const top = sorted.slice(0, 4)
  const rest = sorted.slice(4)
  const segments = top.map(([label, value], i) => ({ label, value, color: SEGMENT_COLORS[i] }))
  if (rest.length)
    segments.push({
      label: `${rest.length} other`,
      value: rest.reduce((sum, [, v]) => sum + v, 0),
      color: 'var(--secondary)',
    })
  return segments
}

function Stat({
  label,
  value,
  unit,
  foot,
  footMono,
  footClassName,
  spark,
}: {
  label: string
  value: string
  unit?: string
  foot?: string
  footMono?: boolean
  footClassName?: string
  spark?: React.ReactNode
}) {
  return (
    <Card className="py-4">
      <CardContent className="px-4">
        <div className="text-muted-foreground text-[11px] font-medium tracking-wider uppercase">
          {label}
        </div>
        <div className="mt-1.5 truncate text-[26px] leading-none font-semibold tracking-tight tabular-nums">
          {value}
          {unit && <span className="text-muted-foreground ml-1 text-[13px] font-medium">{unit}</span>}
        </div>
        {(foot || spark) && (
          <div className="mt-3 flex items-end justify-between gap-2.5">
            <span
              className={`text-muted-foreground truncate text-xs tabular-nums ${footMono ? 'font-mono' : ''} ${footClassName ?? ''}`}
            >
              {foot}
            </span>
            {spark}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
