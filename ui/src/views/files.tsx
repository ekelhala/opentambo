import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { ModelFile, State } from '@/lib/api'
import { bytes, date } from '@/lib/format'
import { PlayIcon, SearchIcon } from 'lucide-react'

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
    <Card>
      <CardHeader className="flex-row items-center">
        <CardTitle>Model inventory</CardTitle>
        <span className="text-muted-foreground ml-auto text-xs font-normal normal-case tracking-normal tabular-nums">
          {bytes(total)} in {state.settings.models}
        </span>
      </CardHeader>
      <CardContent>
        <div className="mb-3.5 flex flex-wrap items-center gap-3">
          <div className="bg-background flex min-w-56 grow items-center gap-2 rounded-lg border px-3 py-1.5 sm:max-w-72 sm:grow-0">
            <SearchIcon className="text-muted-foreground size-3.5 shrink-0" />
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Filter by name or path…"
              aria-label="Filter model files"
              className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            />
          </div>
          <Badge variant="secondary">{state.models.length} GGUF files</Badge>
          <Badge variant="outline">scan of {state.settings.models}</Badge>
        </div>
        {filtered.length ? (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>File</TableHead>
                <TableHead className="w-24 text-right">Size</TableHead>
                <TableHead className="hidden w-32 sm:table-cell">Modified</TableHead>
                <TableHead className="w-44">Status</TableHead>
                <TableHead className="w-28" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((model) => {
                const alias = runningAlias.get(model.path)
                return (
                  <TableRow key={model.path} className="group">
                    <TableCell className="max-w-0">
                      <div className="truncate font-mono text-xs font-medium">{model.path}</div>
                    </TableCell>
                    <TableCell className="text-right text-xs tabular-nums">{bytes(model.size)}</TableCell>
                    <TableCell className="text-muted-foreground hidden text-xs tabular-nums sm:table-cell">
                      {date(model.modified)}
                    </TableCell>
                    <TableCell>
                      {alias ? (
                        <Badge
                          variant="secondary"
                          className="bg-emerald-500/15 font-mono text-emerald-600 dark:text-emerald-400"
                        >
                          ● running · {alias}
                        </Badge>
                      ) : state.profiles[model.path] ? (
                        <Badge variant="outline">profile saved</Badge>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100 max-md:opacity-100">
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`Launch ${model.path}`}
                          disabled={!!alias}
                          onClick={() => onLaunch(model.path)}
                        >
                          <PlayIcon className="size-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          className="text-destructive"
                          disabled={!!alias}
                          onClick={() => onDelete(model)}
                        >
                          Delete
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        ) : (
          <p className="text-muted-foreground py-8 text-center text-sm">
            {state.models.length ? 'No files match' : 'No GGUF files yet.'}
          </p>
        )}
      </CardContent>
    </Card>
  )
}
