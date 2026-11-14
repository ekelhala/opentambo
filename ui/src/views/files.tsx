import { useState } from 'react'
import { PlayIcon, SearchIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import type { ModelFile, State } from '@/lib/api'
import { bytes, date } from '@/lib/format'

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
  const filtered = state.models.filter((m) => m.path.toLowerCase().includes(query.toLowerCase()))
  const runningAlias = new Map(
    state.runs
      .filter((r) => r.status === 'running' || r.status === 'stopping')
      .map((r) => [r.launch.model, r.launch.name])
  )
  const total = state.models.reduce((sum, m) => sum + m.size, 0)
  return (
    <div>
      <h2 className="pane-title">Models</h2>
      <p className="pane-lede">
        Everything on the shelf. The directory is the inventory ·{' '}
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
                placeholder="Filter by name or path…"
                aria-label="Filter model files"
              />
            </div>
            <span className="badge badge-idle">{state.models.length} GGUF files</span>
            <span className="mono text-xs text-[var(--t-text-3)]">scan of {state.settings.models}</span>
          </div>
          {filtered.length ? (
            <table className="table">
              <thead>
                <tr>
                  <th>File</th>
                  <th className="w-24 text-right">Size</th>
                  <th className="hidden w-32 sm:table-cell">Modified</th>
                  <th className="w-44">Status</th>
                  <th className="w-28" />
                </tr>
              </thead>
              <tbody>
                {filtered.map((model) => {
                  const alias = runningAlias.get(model.path)
                  return (
                    <tr key={model.path}>
                      <td className="max-w-0">
                        <span className="fname">{model.path}</span>
                      </td>
                      <td className="num text-right text-xs">{bytes(model.size)}</td>
                      <td className="num hidden text-xs text-[var(--t-text-3)] sm:table-cell">
                        {date(model.modified)}
                      </td>
                      <td>
                        {alias ? (
                          <span className="badge badge-ok">running · {alias}</span>
                        ) : state.profiles[model.path] ? (
                          <span className="badge badge-info">profile saved</span>
                        ) : (
                          <span className="text-[var(--t-text-3)]">—</span>
                        )}
                      </td>
                      <td>
                        <div className="flex justify-end gap-1.5">
                          <Button
                            variant="secondary"
                            size="sm"
                            aria-label={`Launch ${model.path}`}
                            disabled={!!alias}
                            onClick={() => onLaunch(model.path)}
                          >
                            <PlayIcon /> Launch
                          </Button>
                          <Button
                            variant="danger-ghost"
                            size="sm"
                            disabled={!!alias}
                            onClick={() => onDelete(model)}
                          >
                            Delete
                          </Button>
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          ) : (
            <p className="border border-dashed border-[var(--t-line)] py-8 text-center text-sm text-[var(--t-text-2)]">
              {state.models.length ? 'No files match' : 'No GGUF files yet. Queue a download.'}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
