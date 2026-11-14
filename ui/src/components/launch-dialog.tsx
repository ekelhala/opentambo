import { useState } from 'react'
import { PlayIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { api, type Launch, type State } from '@/lib/api'
import { bytes, shellQuote } from '@/lib/format'
import { toast } from 'sonner'

export interface LaunchDraft {
  model?: string
  launch?: Launch
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
  const [model, setModel] = useState(draft.model || draft.launch?.model || '')
  const [name, setName] = useState(profile?.name || '')
  const [context, setContext] = useState(String(profile?.context ?? 8192))
  const [gpu, setGpu] = useState(String(profile?.gpu_layers ?? 99))
  const [port, setPort] = useState(String(profile?.port ?? nextPort(state)))
  const [args, setArgs] = useState((profile?.args || []).join('\n'))
  const [env, setEnv] = useState(
    Object.entries(profile?.env || {})
      .map(([k, v]) => `${k}=${v}`)
      .join('\n')
  )
  const [busy, setBusy] = useState(false)

  const specArgs = args.split('\n').map((s) => s.trim()).filter(Boolean)
  const specName = name.trim() || model.split('/').pop()?.replace(/\.gguf$/i, '') || 'model'
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
          <p className="dialog-lede">
            Your binary, your arguments. The command below is exactly what runs.
          </p>
        </DialogHeader>
        <form onSubmit={submit} className="grid gap-4">
          <div className="field">
            <label htmlFor="launch-model">Model</label>
            <Select value={model} onValueChange={setModel} required>
              <SelectTrigger id="launch-model" className="w-full">
                <SelectValue placeholder="Select a GGUF file" />
              </SelectTrigger>
              <SelectContent>
                {state.models.map((m) => (
                  <SelectItem key={m.path} value={m.path}>
                    {m.path}
                    <span className="select-item-meta">{bytes(m.size)}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="field">
            <label htmlFor="launch-name">API model name</label>
            <input
              id="launch-name"
              className="input mono"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={model.split('/').pop()?.replace(/\.gguf$/i, '')}
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
    if ((run.status === 'running' || run.status === 'stopping') && run.launch.port >= port)
      port = run.launch.port + 1
  return port
}
