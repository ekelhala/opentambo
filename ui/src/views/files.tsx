import { useState } from 'react'
import { ChevronDownIcon, ChevronRightIcon, PlayIcon, SearchIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import type { CatalogModel, ModelFile, Quantization, State } from '@/lib/api'
import { bytes, date } from '@/lib/format'

function quantKey(model: CatalogModel, quant: Quantization): string {
  return `${model.repo}::${quant.family}::${quant.label}::${quant.primary}`
}

/** The shelf, grouped the way models are actually chosen: repository first,
 *  then quantization. Individual shards stay reachable under each group. */
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
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const runningAlias = new Map(
    state.runs
      .filter((r) => r.status === 'running' || r.status === 'stopping')
      .map((r) => [r.launch.model, r.launch.name])
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
          quant.files.some((file) => file.path.toLowerCase().includes(needle))
      ),
    }))
    .filter((model) => model.quantizations.length > 0)

  return (
    <div>
      <h2 className="pane-title">Models</h2>
      <p className="pane-lede">
        Downloaded models by repository and quantization. The directory is the inventory ·{' '}
        <span className="num">{bytes(total)}</span> in {state.settings.models}
      </p>
      <div className="panel">
        <div className="panel-body">
          <div className="mb-3.5 flex flex-wrap items-center gap-3">
            <div className="search min-w-56 grow sm:max-w-72 sm:grow-0">
              <SearchIcon />
              <input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="Filter by repo or quantization…"
                aria-label="Filter models"
              />
            </div>
            <span className="badge badge-idle">
              {state.catalog.length} {state.catalog.length === 1 ? 'model' : 'models'} ·{' '}
              {state.models.length} GGUF files
            </span>
            <span className="mono text-xs text-[var(--t-text-3)]">
              scan of {state.settings.models}
            </span>
          </div>
          {repos.length ? (
            <table className="table">
              <thead>
                <tr>
                  <th>Quantization</th>
                  <th className="w-24 text-right">Size</th>
                  <th className="hidden w-20 sm:table-cell">Files</th>
                  <th className="hidden w-32 sm:table-cell">Modified</th>
                  <th className="w-44">Status</th>
                  <th className="w-48" />
                </tr>
              </thead>
              <tbody>
                {repos.map((model) => (
                  <RepoRows
                    key={model.repo}
                    model={model}
                    runningAlias={runningAlias}
                    expanded={expanded}
                    setExpanded={setExpanded}
                    onLaunch={onLaunch}
                    onDelete={onDelete}
                  />
                ))}
              </tbody>
            </table>
          ) : (
            <p className="border border-dashed border-[var(--t-line)] py-8 text-center text-sm text-[var(--t-text-2)]">
              {state.catalog.length ? 'No models match' : 'No models yet. Queue a download.'}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}

function RepoRows({
  model,
  runningAlias,
  expanded,
  setExpanded,
  onLaunch,
  onDelete,
}: {
  model: CatalogModel
  runningAlias: Map<string, string>
  expanded: Record<string, boolean>
  setExpanded: (value: Record<string, boolean>) => void
  onLaunch: (model: string) => void
  onDelete: (file: ModelFile) => void
}) {
  return (
    <>
      <tr className="border-t border-[var(--t-line-soft)] first:border-t-0">
        <td colSpan={6} className="bg-[var(--t-bg-deep)]">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 py-1">
            <span className="fname font-semibold">{model.repo}</span>
            <span className="badge badge-idle">
              {model.quantizations.length}{' '}
              {model.quantizations.length === 1 ? 'quantization' : 'quantizations'}
            </span>
            <span className="num text-xs text-[var(--t-text-3)]">{bytes(model.size)}</span>
            <span className="mono ml-auto hidden truncate text-xs text-[var(--t-text-3)] lg:inline">
              {model.path}
            </span>
          </div>
        </td>
      </tr>
      {model.quantizations.map((quant) => {
        const key = quantKey(model, quant)
        const open = !!expanded[key]
        const alias = quant.files
          .map((file) => runningAlias.get(file.path))
          .find((name) => name !== undefined)
        return (
          <QuantRows
            key={key}
            quant={quant}
            open={open}
            alias={alias}
            onToggle={() => setExpanded({ ...expanded, [key]: !open })}
            onLaunch={() => onLaunch(quant.primary)}
            onDelete={onDelete}
          />
        )
      })}
    </>
  )
}

function QuantRows({
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
    <>
      <tr>
        <td className="max-w-0">
          <button
            onClick={onToggle}
            aria-expanded={open}
            className="flex w-full min-w-0 items-center gap-2 text-left"
          >
            {open ? <ChevronDownIcon className="h-3.5 w-3.5 shrink-0" /> : <ChevronRightIcon className="h-3.5 w-3.5 shrink-0" />}
            <span className="badge badge-info shrink-0">{quant.label || 'default'}</span>
            {quant.family && !quant.label && (
              <span className="fname min-w-0 truncate" title={quant.family}>
                {quant.family}
              </span>
            )}
            {quant.projection && <span className="badge badge-idle shrink-0">vision mmproj</span>}
          </button>
        </td>
        <td className="num text-right text-xs">{bytes(quant.size)}</td>
        <td className="num hidden text-xs text-[var(--t-text-3)] sm:table-cell">
          {quant.shards}
        </td>
        <td className="num hidden text-xs text-[var(--t-text-3)] sm:table-cell">
          {date(quant.modified)}
        </td>
        <td>
          {alias ? (
            <span className="badge badge-ok">running · {alias}</span>
          ) : quant.projection ? (
            <span className="text-[var(--t-text-3)]">companion file</span>
          ) : (
            <span className="text-[var(--t-text-3)]">—</span>
          )}
        </td>
        <td>
          <div className="flex justify-end gap-1.5">
            <Button
              variant="secondary"
              size="sm"
              aria-label={`Launch ${quant.label || quant.family}`}
              disabled={!!alias || quant.projection}
              onClick={onLaunch}
            >
              <PlayIcon /> Launch
            </Button>
          </div>
        </td>
      </tr>
      {open &&
        quant.files.map((file) => (
          <tr key={file.path} className="text-[var(--t-text-2)]">
            <td className="max-w-0 pl-8">
              <span className="mono block truncate text-xs" title={file.path}>
                {file.path}
              </span>
            </td>
            <td className="num text-right text-xs">{bytes(file.size)}</td>
            <td className="hidden sm:table-cell" />
            <td className="num hidden text-xs text-[var(--t-text-3)] sm:table-cell">
              {date(file.modified)}
            </td>
            <td />
            <td>
              <div className="flex justify-end">
                <Button
                  variant="danger-ghost"
                  size="sm"
                  disabled={!!alias}
                  onClick={() => onDelete(file)}
                >
                  Delete
                </Button>
              </div>
            </td>
          </tr>
        ))}
    </>
  )
}
