export interface Launch {
  name: string
  model: string
  context: number
  gpu_layers: number
  port: number
  args: string[]
  env: Record<string, string>
}

export interface Run {
  id: string
  launch: Launch
  command: string[]
  pid: number
  status: 'running' | 'stopping' | 'stopped' | 'failed'
  started: string
  ended?: string
  error?: string
}

export interface Job {
  id: string
  url: string
  file: string
  status: 'queued' | 'downloading' | 'completed' | 'cancelled' | 'interrupted' | 'failed'
  bytes: number
  total: number
  bytes_per_second: number
  error?: string
  created: string
}

export interface ModelFile {
  path: string
  size: number
  modified: string
}

export interface State {
  settings: { models: string; binary: string; parallel_downloads: number }
  models: ModelFile[]
  jobs: Job[]
  runs: Run[]
  profiles: Record<string, Launch>
}

export async function api<T>(path: string, method: string = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(`/api/${path}`, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  const text = await response.text()
  const data = text ? JSON.parse(text) : undefined
  if (!response.ok) throw new Error(data?.error || `HTTP ${response.status}`)
  return data as T
}

export function repoFromUrl(url: string): string {
  const match = /huggingface\.co\/([^/]+\/[^/]+)\/resolve\//.exec(url)
  return match ? match[1] : ''
}
