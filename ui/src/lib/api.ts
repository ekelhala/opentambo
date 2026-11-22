// Wire types come from the authoritative contract in api/openapi.yaml.
// Regenerate with `make generate-api` (output: ui/src/lib/api.gen.ts).
// Never hand-write transport interfaces here — extend the spec instead.
import type { components } from './api.gen'

export type CacheStatus = components['schemas']['CacheStatus']
export type CacheStats = components['schemas']['CacheStats']
export type CacheView = components['schemas']['CacheSummary']
export type CacheReport = components['schemas']['CacheReport']
export type CacheEntry = components['schemas']['CacheEntry']
export type PromptCacheSpec = components['schemas']['PromptCache']
export type Launch = components['schemas']['Launch']
export type Run = components['schemas']['Run']
export type RunHealth = components['schemas']['RunHealth']
export type Job = components['schemas']['Job']
export type ModelFile = components['schemas']['Model']
export type Quantization = components['schemas']['Quantization']
export type CatalogModel = components['schemas']['CatalogModel']
export type RepositoryFile = components['schemas']['RepositoryFile']
export type RepositoryQuantization = components['schemas']['RepositoryQuantization']
export type RepositoryListing = components['schemas']['RepositoryListing']
export type State = components['schemas']['State']

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
