export interface PromptCacheSpec {
  enabled: boolean
  max_bytes?: number
  max_entry_bytes?: number
  ttl_seconds?: number
  min_reuse_tokens?: number
}

export interface CacheStats {
  requests: number
  hits: number
  misses: number
  live_kept: number
  bypassed: number
  saves: number
  save_failures: number
  restores: number
  restore_failures: number
  queue_timeouts: number
  restore_ms_total: number
  save_ms_total: number
  bytes_restored: number
  bytes_saved: number
}

export interface CacheView {
  requested: boolean
  status: 'checking' | 'ready' | 'unsupported' | 'error'
  namespace?: string
  build?: string
  reason?: string
  min_reuse_tokens?: number
  max_bytes?: number
  stats?: CacheStats
}

export interface Launch {
  name: string
  model: string
  context: number
  gpu_layers: number
  port: number
  args: string[]
  env: Record<string, string>
  prompt_cache?: PromptCacheSpec
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
  cache?: CacheView
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

export interface Quantization {
  label: string
  family: string
  primary: string
  files: ModelFile[]
  size: number
  shards: number
  modified: string
  projection: boolean
}

export interface CatalogModel {
  repo: string
  path: string
  quantizations: Quantization[]
  size: number
  modified: string
}

export interface RepositoryFile {
  name: string
  size: number
}

export interface RepositoryQuantization {
  label: string
  family: string
  files: RepositoryFile[]
  size: number
  shards: number
  projection: boolean
}

export interface State {
  settings: { models: string; binary: string; parallel_downloads: number }
  models: ModelFile[]
  catalog: CatalogModel[]
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
