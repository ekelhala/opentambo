import { WarnIcon } from '@/components/icons'
import { type CacheView, type Run } from '@/lib/api'
import { bytes } from '@/lib/format'

// Prompt cache rendering is driven entirely by the finite status union in
// lib/api: every member must carry an explicit rendering decision. The
// `never` assignments below fail TypeScript checking when a new status
// appears without one.
//
// Copy rules (issue #21): only states where the backend provably keeps
// forwarding may promise that requests run normally. `unsupported` means
// the probe failed and traffic runs uncached; `error` means slot safety
// failed and the triggering request was rejected (502) — inference
// availability is not established there and the backend guarantees no
// automatic recovery, so the panel names the failure and the manual
// recovery action instead of reassuring.

export function CacheBadge({ cache }: { cache?: CacheView | null }) {
  if (!cache?.requested) return null
  switch (cache.status) {
    case 'ready':
      return <span className="badge shrink-0">cache</span>
    case 'checking':
      return <span className="badge badge-info shrink-0">cache…</span>
    case 'unsupported':
      return (
        <span className="badge badge-warn shrink-0" title={cache.reason}>
          no cache
        </span>
      )
    case 'error':
      return (
        <span className="badge badge-warn shrink-0" title={cache.reason}>
          cache error
        </span>
      )
    default: {
      const unhandled: never = cache.status
      return (
        <span className="badge badge-warn shrink-0" title={`Unknown prompt cache status: ${String(unhandled)}`}>
          cache
        </span>
      )
    }
  }
}

// Panel copy for the two failure callouts. Keyed by the status union so
// TypeScript rejects a new failure variant without a rendering decision.
type FailureStatus = 'unsupported' | 'error'

const failureCopy: Record<FailureStatus, { headline: string; fallback: string; outcome: string }> = {
  unsupported: {
    headline: 'Persistent cache not available for this build.',
    fallback: 'The capability check didn\'t pass.',
    outcome: 'Requests keep running without caching.',
  },
  error: {
    headline: 'Prompt cache stopped after a safety failure.',
    fallback: 'The cache couldn\'t put the slot in a known state.',
    outcome: 'Some requests might be rejected while the slot is unsafe. Restart this server to recover.',
  },
}

function CacheCallout({ status, reason }: { status: FailureStatus; reason?: string }) {
  const copy = failureCopy[status]
  return (
    <div className="callout callout-bad m-3 mb-0">
      <WarnIcon />
      <p className="break-words">
        <strong>{copy.headline}</strong> {reason || copy.fallback} {copy.outcome}
      </p>
    </div>
  )
}

function CachePanelBody({ cache }: { cache: CacheView }) {
  const stats = cache.stats
  switch (cache.status) {
    case 'checking':
      return (
        <p className="panel-body text-xs text-[var(--t-text-2)]">
          Checking if this server can save and restore snapshots…
        </p>
      )
    case 'ready':
      if (!stats) return null
      return (
        <div className="panel-body">
          <div className="kv">
            <div>
              <span className="k">Disk hits</span>
              <span className="v">
                {stats.hits}
                <span className="u">restored</span>
              </span>
              <span className="f">
                {stats.restores} restores · {bytes(stats.bytes_restored)}
              </span>
            </div>
            <div>
              <span className="k">Snapshots saved</span>
              <span className="v">
                {stats.saves}
                <span className="u">published</span>
              </span>
              <span className="f">
                {bytes(stats.bytes_saved)} written{stats.save_failures ? ` · ${stats.save_failures} failed` : ''}
                {stats.save_refused_budget ? ` · ${stats.save_refused_budget} refused (no budget room)` : ''}
                {stats.save_replacements ? ` · ${stats.save_replacements} superseded replaced` : ''}
                {stats.save_evictions ? ` · ${stats.save_evictions} cold evicted` : ''}
              </span>
            </div>
            <div>
              <span className="k">Live reuse</span>
              <span className="v">
                {stats.live_kept}
                <span className="u">kept</span>
              </span>
              <span className="f">{stats.requests} coordinated requests</span>
            </div>
            <div>
              <span className="k">Cold / bypassed</span>
              <span className="v">
                {stats.misses + stats.bypassed}
                <span className="u">plain</span>
              </span>
              <span className="f">
                {cache.min_reuse_tokens?.toLocaleString()} token min reuse · {stats.restore_failures} bad restores
              </span>
            </div>
          </div>
          {cache.last_error ? (
            <p className="break-all pt-2 text-xs text-[var(--t-text-2)]">
              Last snapshot save failure: {cache.last_error}
            </p>
          ) : null}
        </div>
      )
    case 'unsupported':
    case 'error':
      return <CacheCallout status={cache.status} reason={cache.reason} />
    default: {
      const unhandled: never = cache.status
      return (
        <p className="panel-body text-xs text-[var(--t-text-2)]">Unknown prompt cache status: {String(unhandled)}</p>
      )
    }
  }
}

export function CachePanel({ run }: { run: Run }) {
  const cache = run.cache
  if (!cache) return null
  return (
    <div className="panel">
      <div className="panel-head">
        <span className="panel-title">Prompt cache</span>
        <CacheBadge cache={cache} />
        <span className="panel-meta">
          {[cache.build ? `llama.cpp ${cache.build}` : '', cache.namespace ? `ns ${cache.namespace}` : '']
            .filter(Boolean)
            .join(' · ')}
        </span>
      </div>
      <CachePanelBody cache={cache} />
    </div>
  )
}
