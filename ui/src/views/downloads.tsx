import { useState } from 'react'
import { AreaChart } from '@/components/sparkline'
import { StatusPill } from '@/components/status-pill'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { bytes, duration } from '@/lib/format'
import { DownloadIcon } from 'lucide-react'
import type { Job, State } from '@/lib/api'

export function DownloadsView({
  state,
  bandwidth,
  onSubmit,
  onCall,
}: {
  state: State
  bandwidth: number[]
  onSubmit: (body: { repo: string; files: string[]; revision: string }) => void
  onCall: (path: string, method: string, body?: unknown, ok?: string) => void
}) {
  const active = state.jobs.filter((j) => j.status === 'downloading')
  const now = active.reduce((sum, j) => sum + j.bytes_per_second, 0)
  return (
    <div className="grid gap-3.5">
      <div className="grid gap-3.5 lg:grid-cols-3">
        <DownloadForm
          className="lg:col-span-2"
          slots={`${active.length} / ${state.settings.parallel_downloads}`}
          onSubmit={onSubmit}
        />
        <Card>
          <CardHeader className="flex-row items-center">
            <CardTitle>Bandwidth</CardTitle>
            <span className="text-muted-foreground ml-auto font-mono text-xs font-normal normal-case tracking-normal tabular-nums">
              {bytes(now)}/s
            </span>
          </CardHeader>
          <CardContent>
            <AreaChart values={bandwidth} height={150} />
          </CardContent>
        </Card>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Jobs</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4">
          {state.jobs.length ? (
            state.jobs.map((job) => <JobRow key={job.id} job={job} onCall={onCall} />)
          ) : (
            <p className="text-muted-foreground py-6 text-center text-sm">No downloads yet.</p>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function DownloadForm({
  className,
  slots,
  onSubmit,
}: {
  className?: string
  slots: string
  onSubmit: (body: { repo: string; files: string[]; revision: string }) => void
}) {
  const [repo, setRepo] = useState('')
  const [files, setFiles] = useState('')
  const [revision, setRevision] = useState('')
  return (
    <Card className={className}>
      <CardHeader className="flex-row items-center">
        <CardTitle>New download</CardTitle>
        <span className="text-muted-foreground ml-auto text-xs font-normal normal-case tracking-normal">
          public Hugging Face repos · exact GGUF filenames
        </span>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-3"
          onSubmit={(event) => {
            event.preventDefault()
            onSubmit({
              repo: repo.trim(),
              files: files.split('\n').map((s) => s.trim()).filter(Boolean),
              revision: revision.trim(),
            })
            setFiles('')
          }}
        >
          <div className="grid gap-3 sm:grid-cols-[1fr_140px]">
            <div className="grid gap-1.5">
              <Label htmlFor="dl-repo">Repository</Label>
              <Input
                id="dl-repo"
                required
                placeholder="owner/repo"
                value={repo}
                onChange={(event) => setRepo(event.target.value)}
                spellCheck={false}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="dl-rev">
                Revision <span className="text-muted-foreground font-normal">(optional)</span>
              </Label>
              <Input
                id="dl-rev"
                placeholder="main"
                value={revision}
                onChange={(event) => setRevision(event.target.value)}
                spellCheck={false}
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="dl-files">
              Files <span className="text-muted-foreground font-normal">(one per line, subdirectories allowed)</span>
            </Label>
            <Textarea
              id="dl-files"
              required
              rows={2}
              placeholder="model-q4_k_m.gguf"
              value={files}
              onChange={(event) => setFiles(event.target.value)}
              spellCheck={false}
              className="font-mono text-xs"
            />
            <p className="text-muted-foreground text-xs">
              Transfers land as <span className="font-mono">.part</span> files and are verified before
              the final name is claimed. Existing files are never replaced.
            </p>
          </div>
          <div className="flex items-center gap-3 pt-1">
            <Button type="submit">
              <DownloadIcon className="size-4" /> Queue download
            </Button>
            <span className="text-muted-foreground text-xs tabular-nums">
              Parallel slots <b className="text-foreground font-semibold">{slots}</b> in use
            </span>
          </div>
        </form>
      </CardContent>
    </Card>
  )
}

function JobRow({
  job,
  onCall,
}: {
  job: Job
  onCall: (path: string, method: string, body?: unknown, ok?: string) => void
}) {
  const pct = job.total > 0 ? Math.min(100, (job.bytes / job.total) * 100) : 0
  const eta =
    job.status === 'downloading' && job.bytes_per_second > 0
      ? duration((job.total - job.bytes) / job.bytes_per_second)
      : null
  return (
    <div className="flex items-start gap-3.5">
      <span className="pt-0.5">
        <StatusPill status={job.status} />
      </span>
      <div className="min-w-0 grow">
        <div className="truncate font-mono text-[13px] font-semibold">{job.file}</div>
        {/* progress */}
        <div className="mt-2">
          <div className="bg-secondary h-1.5 overflow-hidden rounded-full">
            <span
              className={
                job.status === 'completed'
                  ? 'block h-full rounded-full bg-emerald-500 transition-[width] duration-700'
                  : job.status === 'interrupted' || job.status === 'failed'
                    ? 'block h-full rounded-full bg-destructive transition-[width] duration-700'
                    : 'block h-full rounded-full bg-sky-500 transition-[width] duration-700'
              }
              style={{ width: `${pct}%` }}
            />
          </div>
          <div className="text-muted-foreground mt-1 flex justify-between font-mono text-[11px] tabular-nums">
            <span>
              {bytes(job.bytes)}
              {job.total > 0 ? ` / ${bytes(job.total)}` : ''} · {pct.toFixed(0)}%
            </span>
            <span className="min-w-0 truncate">
              {job.bytes_per_second > 0
                ? `${bytes(job.bytes_per_second)}/s${eta ? ` · ETA ${eta}` : ''}`
                : job.status === 'completed'
                  ? 'verified · final name claimed'
                  : job.status === 'queued'
                    ? 'waiting for a slot'
                    : 'paused'}
            </span>
          </div>
        </div>
        {job.error && <p className="text-destructive mt-1.5 font-mono text-[11px] break-all">{job.error}</p>}
      </div>
      <span className="flex shrink-0 gap-1.5 pt-0.5">
        {job.status === 'downloading' || job.status === 'queued' ? (
          <Button
            variant="outline"
            size="sm"
            className="text-destructive"
            onClick={() => onCall(`downloads/${job.id}/cancel`, 'POST', {}, 'Cancelled')}
          >
            {job.status === 'queued' ? 'Remove' : 'Cancel'}
          </Button>
        ) : job.status !== 'completed' ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => onCall(`downloads/${job.id}/resume`, 'POST', {}, 'Resumed')}
          >
            Resume
          </Button>
        ) : null}
      </span>
    </div>
  )
}
