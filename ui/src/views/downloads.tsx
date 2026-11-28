import { useState } from 'react'
import { CloseIcon, DownloadIcon, PlayIcon, SearchIcon, SpinnerIcon } from '@/components/icons'
import { AreaChart } from '@/components/sparkline'
import { StatusPill } from '@/components/status-pill'
import { Button } from '@/components/ui/button'
import { api, type RepositoryQuantization } from '@/lib/api'
import { bytes, duration } from '@/lib/format'
import type { Job, State } from '@/lib/api'
import { toast } from 'sonner'

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
    <div className="grid grid-cols-[minmax(0,1fr)] gap-3.5">
      <h2 className="pane-title">Downloads</h2>
      <p className="pane-lede">Pull GGUF files from public Hugging Face repos into the shelf.</p>
      <div className="grid grid-cols-[minmax(0,1fr)] gap-3.5 lg:grid-cols-3">
        <DownloadForm
          className="lg:col-span-2"
          slots={`${active.length} / ${state.settings.parallel_downloads}`}
          onSubmit={onSubmit}
        />
        <div className="panel">
          <div className="panel-head">
            <span className="panel-title">Bandwidth</span>
            <span className="panel-meta num">{bytes(now)}/s</span>
          </div>
          <div className="panel-body">
            <AreaChart values={bandwidth} height={150} />
          </div>
        </div>
      </div>
      <div className="panel">
        <div className="panel-head">
          <span className="panel-title">Jobs</span>
          <span className="panel-meta num">{state.jobs.length} total</span>
        </div>
        <div className="panel-body">
          {state.jobs.length ? (
            state.jobs.map((job) => <JobRow key={job.id} job={job} onCall={onCall} />)
          ) : (
            <p className="border border-dashed border-[var(--t-line)] py-8 text-center text-sm text-[var(--t-text-2)]">
              No downloads yet. Queue one above.
            </p>
          )}
        </div>
      </div>
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
  const [quants, setQuants] = useState<RepositoryQuantization[] | null>(null)
  const [lookupBusy, setLookupBusy] = useState(false)
  const repoLooksValid = /^[\w.-]+\/[\w.-]+$/.test(repo.trim())

  async function lookUp() {
    const trimmed = repo.trim()
    if (!repoLooksValid) return
    setLookupBusy(true)
    try {
      const query = new URLSearchParams({ repo: trimmed })
      if (revision.trim()) query.set('revision', revision.trim())
      const result = await api<{ quantizations: RepositoryQuantization[] }>(`repository?${query.toString()}`)
      setQuants(result.quantizations)
      if (!result.quantizations.length) toast.message('No GGUF quantizations found in that repository')
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error))
    } finally {
      setLookupBusy(false)
    }
  }

  function pick(quant: RepositoryQuantization) {
    setFiles(quant.files.map((file) => file.name).join('\n'))
  }

  return (
    <div className={`panel ${className ?? ''}`}>
      <div className="panel-head">
        <span className="panel-title">New download</span>
        <span className="panel-meta max-sm:hidden">public repos — pick a quantization and shards are handled for you</span>
      </div>
      <div className="panel-body">
        <form
          className="grid grid-cols-[minmax(0,1fr)] gap-3"
          onSubmit={(event) => {
            event.preventDefault()
            onSubmit({
              repo: repo.trim(),
              files: files
                .split('\n')
                .map((s) => s.trim())
                .filter(Boolean),
              revision: revision.trim(),
            })
            setFiles('')
          }}
        >
          <div className="grid gap-3 sm:grid-cols-[1fr_140px_auto] sm:items-end">
            <div className="field">
              <label htmlFor="dl-repo">Repository</label>
              <input
                id="dl-repo"
                className="input mono"
                required
                placeholder="owner/repo"
                value={repo}
                onChange={(event) => {
                  setRepo(event.target.value)
                  setQuants(null)
                }}
                spellCheck={false}
              />
            </div>
            <div className="field">
              <label htmlFor="dl-rev">
                Revision <span className="text-[var(--t-text-3)]">(optional)</span>
              </label>
              <input
                id="dl-rev"
                className="input mono"
                placeholder="main"
                value={revision}
                onChange={(event) => setRevision(event.target.value)}
                spellCheck={false}
              />
            </div>
            <Button type="button" variant="secondary" onClick={lookUp} disabled={lookupBusy || !repoLooksValid}>
              {lookupBusy ? <SpinnerIcon /> : <SearchIcon />} Find quantizations
            </Button>
          </div>
          {quants && quants.length > 0 && (
            <div
              className="grid grid-cols-[minmax(0,1fr)] gap-1.5"
              role="list"
              aria-label="Quantizations in the repository"
            >
              {quants.map((quant) => (
                <button
                  key={`${quant.label}-${quant.family}-${quant.files[0]?.name}`}
                  type="button"
                  role="listitem"
                  onClick={() => pick(quant)}
                  className="flex flex-wrap items-center gap-x-3 gap-y-1 border border-[var(--t-line)] bg-[var(--t-bg-raised,var(--t-bg))] px-3 py-2 text-left text-xs transition-colors hover:border-[var(--t-accent)]"
                >
                  <span className="badge badge-info shrink-0">{quant.label || 'default'}</span>
                  {quant.family && <span className="mono truncate">{quant.family}</span>}
                  {quant.projection && <span className="badge badge-idle shrink-0">vision mmproj</span>}
                  <span className="num ml-auto shrink-0 text-[var(--t-text-2)]">
                    {bytes(quant.size)}
                    {quant.shards > 1 ? ` · ${quant.shards} shards` : ''}
                  </span>
                </button>
              ))}
            </div>
          )}
          <div className="field">
            <label htmlFor="dl-files">
              Files <span className="text-[var(--t-text-3)]">(one per line, subdirectories allowed)</span>
            </label>
            <textarea
              id="dl-files"
              className="textarea mono"
              required
              rows={2}
              placeholder="model-q4_k_m.gguf"
              value={files}
              onChange={(event) => setFiles(event.target.value)}
              spellCheck={false}
            />
            <p className="f-hint">
              Pick a quantization above to fill this in, or type exact filenames. Transfers land as{' '}
              <span className="mono">.part</span> files and are verified before the final name is claimed. Existing
              files are never replaced.
            </p>
          </div>
          <div className="flex items-center gap-3 pt-1">
            <Button type="submit">
              <DownloadIcon /> Queue download
            </Button>
            <span className="num text-xs text-[var(--t-text-3)]">
              Slots <b className="text-[var(--t-text)]">{slots}</b> in use
            </span>
          </div>
        </form>
      </div>
    </div>
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
  const tone =
    job.status === 'completed'
      ? 'ok'
      : job.status === 'interrupted' || job.status === 'failed'
        ? 'bad'
        : job.status === 'queued'
          ? 'warn'
          : 'info'
  const repo = job.url.replace(/^https?:\/\/[^/]+\//, '').replace(/\/resolve\/.*$/, '')
  return (
    <div className="dl-row">
      <div className="dl-main min-w-0">
        <span className="fname font-semibold">{job.file}</span>
        <div className={`progress ${tone}`}>
          <i style={{ width: `${pct}%` }} />
        </div>
        <div className="dl-stats">
          <span>
            {bytes(job.bytes)}
            {job.total > 0 ? ` / ${bytes(job.total)}` : ''} · {pct.toFixed(0)}%
          </span>
          <span className="min-w-0 truncate">
            {job.bytes_per_second > 0
              ? `${bytes(job.bytes_per_second)}/s`
              : job.status === 'completed'
                ? 'verified · final name claimed'
                : job.status === 'queued'
                  ? 'waiting for a slot'
                  : 'paused'}
          </span>
        </div>
        {job.error && <p className="mono mt-1.5 text-[11px] break-all text-[var(--t-bad-text)]">{job.error}</p>}
      </div>
      <StatusPill status={job.status} />
      <span className="num truncate text-xs text-[var(--t-text-3)]" title={repo}>
        {repo || '—'}
      </span>
      <span className="num text-xs text-[var(--t-text-3)]">
        {eta ? `ETA ${eta}` : job.status === 'completed' ? 'stored' : '—'}
      </span>
      {job.status === 'downloading' || job.status === 'queued' ? (
        <button
          className="icon-btn"
          aria-label={job.status === 'queued' ? `Remove ${job.file}` : `Cancel ${job.file}`}
          onClick={() => onCall(`downloads/${job.id}/cancel`, 'POST', {}, 'Cancelled')}
        >
          <CloseIcon />
        </button>
      ) : job.status !== 'completed' ? (
        <button
          className="icon-btn"
          aria-label={`Resume ${job.file}`}
          onClick={() => onCall(`downloads/${job.id}/resume`, 'POST', {}, 'Resumed')}
        >
          <PlayIcon />
        </button>
      ) : (
        <span />
      )}
    </div>
  )
}
