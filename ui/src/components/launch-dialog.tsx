import { useState } from 'react'
import { PlayIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { api, type Launch, type State } from '@/lib/api'
import { bytes, modelStem, shellQuote } from '@/lib/format'
import { toast } from 'sonner'

export interface LaunchDraft {
  model?: string
  launch?: Launch
}

interface ModelOption {
  primary: string
  repo: string
  label: string
  size: number
  shards: number
}

/** Launchable options from the catalog: one per repository + quantization.
 *  The system resolves which file (the first shard) actually runs. */
function modelOptions(state: State): ModelOption[] {
  return state.catalog.flatMap((model) =>
    model.quantizations
      .filter((quant) => !quant.projection)
      .map((quant) => ({
        primary: quant.primary,
        repo: model.repo,
        label: quant.label || quant.family || 'default',
        size: quant.size,
        shards: quant.shards,
      })),
  )
}

export function LaunchDialog({
  state,
  draft,
  onClose,
  onLaunched,
}: {
  state: State
  draft: LaunchDraft
  onClose: () => void
  onLaunched: () => void
}) {
  const profile = (draft.model && state.profiles[draft.model]) || draft.launch
  const options = modelOptions(state)
  const [model, setModel] = useState(draft.model || draft.launch?.model || '')
  const selected = options.find((option) => option.primary === model)
  const [name, setName] = useState(profile?.name || '')
  const [context, setContext] = useState(String(profile?.context ?? 8192))
  const [gpu, setGpu] = useState(String(profile?.gpu_layers ?? 99))
  const [port, setPort] = useState(String(profile?.port ?? nextPort(state)))
  const [args, setArgs] = useState((profile?.args || []).join('\n'))
  const [env, setEnv] = useState(
    Object.entries(profile?.env || {})
      .map(([k, v]) => `${k}=${v}`)
      .join('\n'),
  )
  const [promptCache, setPromptCache] = useState(Boolean(profile?.prompt_cache?.enabled))
  const [busy, setBusy] = useState(false)
  // Advanced cache settings have no editing controls here; carrying the saved
  // profile's values through keeps "Run again" from silently resetting them.
  const savedCache = profile?.prompt_cache

  const specArgs = args
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
  const specName = name.trim() || (model ? modelStem(model) : 'model')
  const command = [
    state.settings.binary,
    '--model',
    `${state.settings.models}/${model || '…'}`,
    '--ctx-size',
    context,
    '--n-gpu-layers',
    gpu,
    ...specArgs,
    '--alias',
    specName,
    '--host',
    '127.0.0.1',
    '--port',
    port,
  ]
    .map(shellQuote)
    .join(' ')

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    const envMap: Record<string, string> = {}
    for (const line of env.split('\n')) {
      if (!line.trim()) continue
      const index = line.indexOf('=')
      if (index < 1) {
        toast.error('Environment must be NAME=value per line')
        return
      }
      envMap[line.slice(0, index).trim()] = line.slice(index + 1)
    }
    setBusy(true)
    try {
      await api<Launch>('runs', 'POST', {
        name: specName,
        model,
        context: Number(context),
        gpu_layers: Number(gpu),
        port: Number(port),
        args: specArgs,
        env: envMap,
        prompt_cache: { ...savedCache, enabled: promptCache },
      } satisfies Launch)
      toast.success(`${specName} launched`)
      onLaunched()
      onClose()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="dialog-title">Launch server</DialogTitle>
          <p className="dialog-lede">Your binary, your arguments. The command below is exactly what runs.</p>
        </DialogHeader>
        <form onSubmit={submit} className="grid grid-cols-[minmax(0,1fr)] gap-4">
          <div className="field">
            <label htmlFor="launch-model">Model</label>
            <Select value={model} onValueChange={setModel} required>
              <SelectTrigger id="launch-model" className="w-full">
                <SelectValue placeholder="Select a downloaded model" />
              </SelectTrigger>
              <SelectContent>
                {options.map((option) => (
                  <SelectItem key={`${option.repo} ${option.label} ${option.primary}`} value={option.primary}>
                    <span className="flex min-w-0 flex-wrap items-center gap-x-2">
                      <span className="truncate">{option.repo}</span>
                      <span className="badge badge-info shrink-0">{option.label}</span>
                    </span>
                    <span className="select-item-meta">
                      {bytes(option.size)}
                      {option.shards > 1 ? ` · ${option.shards} shards` : ''}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {selected && selected.shards > 1 && (
              <p className="f-hint">
                Split model · llama-server loads all {selected.shards} shards starting from{' '}
                <span className="mono">{selected.primary.split('/').pop()}</span>
              </p>
            )}
          </div>
          <div className="field">
            <label htmlFor="launch-name">API model name</label>
            <input
              id="launch-name"
              className="input mono"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={model ? modelStem(model) : undefined}
            />
          </div>
          <div className="grid grid-cols-3 gap-3">
            <div className="field">
              <label htmlFor="launch-context">Context</label>
              <input
                id="launch-context"
                className="input num"
                type="number"
                min={1}
                required
                value={context}
                onChange={(e) => setContext(e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="launch-gpu">GPU layers</label>
              <input
                id="launch-gpu"
                className="input num"
                type="number"
                min={0}
                required
                value={gpu}
                onChange={(e) => setGpu(e.target.value)}
              />
            </div>
            <div className="field">
              <label htmlFor="launch-port">Port</label>
              <input
                id="launch-port"
                className="input num"
                type="number"
                min={1024}
                max={65535}
                required
                value={port}
                onChange={(e) => setPort(e.target.value)}
              />
            </div>
          </div>
          <div className="field">
            <label htmlFor="launch-args">Arguments</label>
            <textarea
              id="launch-args"
              className="textarea mono"
              rows={3}
              value={args}
              onChange={(e) => setArgs(e.target.value)}
              placeholder={'--parallel\n1\n--jinja'}
              spellCheck={false}
            />
          </div>
          <div className="field">
            <label htmlFor="launch-env">Environment</label>
            <textarea
              id="launch-env"
              className="textarea mono"
              rows={2}
              value={env}
              onChange={(e) => setEnv(e.target.value)}
              placeholder="CUDA_VISIBLE_DEVICES=0"
              spellCheck={false}
            />
          </div>
          <label className="flex items-start gap-2.5 rounded-md border border-[var(--t-border)] p-3 text-xs">
            <input
              type="checkbox"
              checked={promptCache}
              onChange={(e) => setPromptCache(e.target.checked)}
              className="mt-0.5"
            />
            <span>
              <span className="f-label">Persistent prompt cache</span>
              <p className="mt-0.5 text-[var(--t-text-2)]">
                Save and restore KV state on disk so conversations pick up where they left off, even across app switches
                and server restarts. Requests to this server run one at a time, and{' '}
                <span className="mono">--slot-save-path</span> is handled automatically.
              </p>
            </span>
          </label>
          <div className="field">
            <span className="f-label">Command</span>
            <pre className="well wrap">{command}</pre>
          </div>
          <DialogFooter>
            <Button type="button" variant="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || !model}>
              <PlayIcon /> {busy ? 'Launching…' : 'Launch'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function nextPort(state: State): number {
  let port = 8080
  for (const run of state.runs)
    if ((run.status === 'running' || run.status === 'stopping') && run.launch.port >= port) port = run.launch.port + 1
  return port
}
