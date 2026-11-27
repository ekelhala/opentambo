import { useState } from 'react'
import { ChevronDownIcon, ChevronRightIcon, PlayIcon, SearchIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import type { CatalogModel, ModelFile, Quantization, State } from '@/lib/api'
import { bytes, date } from '@/lib/format'

function quantKey(model: CatalogModel, quant: Quantization): string {
  return `${model.repo}::${quant.family}::${quant.label}::${quant.primary}`
}

/** The shelf, grouped the way models are actually chosen: one quiet row per
 *  repository, quantizations behind a tap, individual shards behind another.
 *  Nothing is expanded until the operator asks for it. */
export function FilesView({
  state,
  onLaunch,
  onDelete,
}: {
  state: State
  onLaunch: (model: string) => void
  onDelete: (file: ModelFile) => void
}) {
  const [query, setQuery] = useState('')
  const [openRepos, setOpenRepos] = useState<Record<string, boolean>>({})
  const [openQuants, setOpenQuants] = useState<Record<string, boolean>>({})
  const runningAlias = new Map(
    state.runs
      .filter((r) => r.status === 'running' || r.status === 'stopping')
      .map((r) => [r.launch.model, r.launch.name]),
  )
  const total = state.models.reduce((sum, m) => sum + m.size, 0)
  const needle = query.toLowerCase()
  const repos = state.catalog
    .map((model) => ({
      ...model,
      quantizations: model.quantizations.filter(
        (quant) =>
          !needle ||
          model.repo.toLowerCase().includes(needle) ||
          quant.label.toLowerCase().includes(needle) ||
          quant.family.toLowerCase().includes(needle) ||
          quant.files.some((file) => file.path.toLowerCase().includes(needle)),
      ),
    }))
    .filter((model) => model.quantizations.length > 0)

  return (
    <div>
      <h2 className="pane-title">Models</h2>
      <p className="pane-lede">
        {state.catalog.length} {state.catalog.length === 1 ? 'model' : 'models'} ·{' '}
        <span className="num">{bytes(total)}</span> — open a repository to launch a quantization.
      </p>
      <div className="search mb-3.5 max-w-96">
        <SearchIcon />
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Filter by repo or quantization…"
          aria-label="Filter models"
        />
      </div>
      {repos.length ? (
        <div className="panel">
          {repos.map((model) => {
            // searching opens repos by default, but an explicit close still wins
            const repoOpen = needle ? openRepos[model.repo] !== false : !!openRepos[model.repo]
            return (
              <RepoGroup
                key={model.repo}
                model={model}
                runningAlias={runningAlias}
                repoOpen={repoOpen}
                onToggleRepo={() => setOpenRepos({ ...openRepos, [model.repo]: !repoOpen })}
                openQuants={openQuants}
                setOpenQuants={setOpenQuants}
                onLaunch={onLaunch}
                onDelete={onDelete}
              />
            )
          })}
        </div>
      ) : (
        <p className="border border-dashed border-[var(--t-line)] py-8 text-center text-sm text-[var(--t-text-2)]">
          {state.catalog.length ? 'No models match' : 'No models yet. Queue a download.'}
        </p>
      )}
    </div>
  )
}

function RepoGroup({
  model,
  runningAlias,
  repoOpen,
  onToggleRepo,
  openQuants,
  setOpenQuants,
  onLaunch,
  onDelete,
}: {
  model: CatalogModel
  runningAlias: Map<string, string>
  repoOpen: boolean
  onToggleRepo: () => void
  openQuants: Record<string, boolean>
  setOpenQuants: (value: Record<string, boolean>) => void
  onLaunch: (model: string) => void
  onDelete: (file: ModelFile) => void
}) {
  const aliases = model.quantizations
    .flatMap((quant) => quant.files.map((file) => runningAlias.get(file.path)))
    .filter((name): name is string => name !== undefined)
  return (
    <section className="border-b border-[var(--t-line)] last:border-b-0">
      <button
        onClick={onToggleRepo}
        aria-expanded={repoOpen}
        className="repo-head flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-left"
      >
        <span className="fname min-w-0 grow truncate font-semibold">{model.repo}</span>
        <span className="num shrink-0 text-xs text-[var(--t-text-3)]">{bytes(model.size)}</span>
        {repoOpen ? (
          <ChevronDownIcon className="size-4 shrink-0 text-[var(--t-text-3)]" />
        ) : (
          <ChevronRightIcon className="size-4 shrink-0 text-[var(--t-text-3)]" />
        )}
        <span className="mono flex w-full items-center gap-2 text-xs text-[var(--t-text-3)]">
          {aliases.length > 0 && (
            <span className="badge badge-ok shrink-0">running{aliases[0] ? ` · ${aliases[0]}` : ''}</span>
          )}
          {!repoOpen && (
            <>
              {model.quantizations.length} {model.quantizations.length === 1 ? 'quantization' : 'quantizations'}
            </>
          )}
        </span>
      </button>
      {repoOpen &&
        model.quantizations.map((quant) => {
          const key = quantKey(model, quant)
          return (
            <QuantRow
              key={key}
              quant={quant}
              open={!!openQuants[key]}
              alias={quant.files.map((file) => runningAlias.get(file.path)).find((name) => name !== undefined)}
              onToggle={() => setOpenQuants({ ...openQuants, [key]: !openQuants[key] })}
              onLaunch={() => onLaunch(quant.primary)}
              onDelete={onDelete}
            />
          )
        })}
    </section>
  )
}

function QuantRow({
  quant,
  open,
  alias,
  onToggle,
  onLaunch,
  onDelete,
}: {
  quant: Quantization
  open: boolean
  alias?: string
  onToggle: () => void
  onLaunch: () => void
  onDelete: (file: ModelFile) => void
}) {
  return (
    <div className="quant-row border-t border-[var(--t-line-soft)]">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2.5">
        <button
          onClick={onToggle}
          aria-expanded={open}
          className="flex min-w-0 grow flex-wrap items-center gap-2 text-left"
        >
          {open ? (
            <ChevronDownIcon className="size-3.5 shrink-0 text-[var(--t-text-3)]" />
          ) : (
            <ChevronRightIcon className="size-3.5 shrink-0 text-[var(--t-text-3)]" />
          )}
          <span className="badge badge-info shrink-0">{quant.label || 'default'}</span>
          {quant.family && !quant.label && (
            <span className="mono min-w-0 truncate text-xs text-[var(--t-text-2)]" title={quant.family}>
              {quant.family}
            </span>
          )}
          {quant.projection && <span className="badge badge-idle shrink-0">companion file</span>}
          {alias && <span className="badge badge-ok shrink-0">running · {alias}</span>}
        </button>
        <span className="num shrink-0 text-xs text-[var(--t-text-2)]">{bytes(quant.size)}</span>
        {!alias && !quant.projection && (
          <Button variant="secondary" size="sm" aria-label={`Launch ${quant.label || quant.family}`} onClick={onLaunch}>
            <PlayIcon /> Launch
          </Button>
        )}
      </div>
      {open &&
        quant.files.map((file) => (
          <div
            key={file.path}
            className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-[var(--t-line-soft)] bg-[var(--t-bg-deep)] px-4 py-2 pl-10"
          >
            <span className="mono min-w-0 grow basis-40 truncate text-xs text-[var(--t-text-2)]" title={file.path}>
              {file.path}
            </span>
            <span className="num shrink-0 text-xs text-[var(--t-text-3)]">{bytes(file.size)}</span>
            <span className="num shrink-0 text-xs text-[var(--t-text-3)] max-sm:hidden">{date(file.modified)}</span>
            <Button variant="danger-ghost" size="sm" disabled={!!alias} onClick={() => onDelete(file)}>
              Delete
            </Button>
          </div>
        ))}
    </div>
  )
}
