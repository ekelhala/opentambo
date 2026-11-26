import { expect, test } from 'bun:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { CacheBadge, CachePanel } from '@/components/cache-status'
import type { CacheStats, CacheView, Run } from '@/lib/api'

// Issue #21: every cache status in the finite union must render a distinct,
// truthful panel and badge, including visible reasons and missing-reason
// fallbacks. Compile-time exhaustiveness lives in cache-status.tsx (never
// assignments); these fixtures pin the runtime behavior for all four states.

const stats: CacheStats = {
  requests: 12,
  hits: 3,
  misses: 4,
  live_kept: 2,
  bypassed: 3,
  saves: 5,
  save_failures: 1,
  save_refused_budget: 1,
  save_replacements: 2,
  restores: 4,
  restore_failures: 0,
  queue_timeouts: 0,
  restore_ms_total: 40,
  save_ms_total: 90,
  bytes_restored: 2048,
  bytes_saved: 4096,
}

function makeRun(cache: CacheView): Run {
  return {
    id: 'run-1',
    launch: { name: 'tester', model: 'model.gguf', context: 8192, gpu_layers: 99, port: 8080, args: [], env: {} },
    command: ['llama-server'],
    pid: 4242,
    status: 'running',
    started: '2026-11-15T10:00:00Z',
    cache,
  }
}

const badge = (cache: CacheView) => renderToStaticMarkup(<CacheBadge cache={cache} />)
const panel = (cache: CacheView) => renderToStaticMarkup(<CachePanel run={makeRun(cache)} />)

test('badge renders nothing when no cache was requested', () => {
  expect(badge({ requested: false, status: 'checking' })).toBe('')
  expect(badge({ requested: false, status: 'error' })).toBe('')
})

test('badge shows one distinct state for every status', () => {
  const checking = badge({ requested: true, status: 'checking' })
  expect(checking).toContain('badge-info')
  expect(checking).toContain('cache…')

  const ready = badge({ requested: true, status: 'ready' })
  expect(ready).toContain('>cache<')
  expect(ready).not.toContain('badge-warn')

  const unsupported = badge({ requested: true, status: 'unsupported', reason: 'no slots api' })
  expect(unsupported).toContain('badge-warn')
  expect(unsupported).toContain('no cache')
  expect(unsupported).toContain('no slots api')

  const failure = badge({ requested: true, status: 'error', reason: 'slot stuck' })
  expect(failure).toContain('badge-warn')
  expect(failure).toContain('cache error')
  expect(failure).toContain('slot stuck')
})

test('panel checking state reports the in-flight probe', () => {
  const html = panel({ requested: true, status: 'checking' })
  expect(html).toContain('Verifying save/restore capability')
})

test('panel ready state renders the usage stats', () => {
  const html = panel({ requested: true, status: 'ready', stats, min_reuse_tokens: 64 })
  expect(html).toContain('Disk hits')
  expect(html).toContain('Snapshots saved')
  expect(html).toContain('3') // hits
  expect(html).toContain('4 restores')
  expect(html).toContain('2 kB') // bytes_restored
})

// Issue #33: save failures must be attributable, not hidden behind the
// aggregate counter. Refusals and replacements get their own figures and
// the latest failure names its kind.
test('panel ready state attributes save failures', () => {
  const html = panel({
    requested: true,
    status: 'ready',
    stats,
    min_reuse_tokens: 64,
    last_error: 'snapshot save: reservation refused: no snapshot budget headroom',
  })
  expect(html).toContain('1 refused (no budget room)')
  expect(html).toContain('2 superseded replaced')
  expect(html).toContain('Last snapshot save failure')
  expect(html).toContain('reservation refused')
})

test('panel ready state stays clean without a save failure', () => {
  const html = panel({
    requested: true,
    status: 'ready',
    stats: { ...stats, save_failures: 0, save_refused_budget: 0, save_replacements: 0 },
  })
  expect(html).not.toContain('Last snapshot save failure')
  expect(html).not.toContain('refused')
})

test('panel unsupported distinguishes plain forwarding and shows the reason', () => {
  const withReason = panel({ requested: true, status: 'unsupported', reason: 'slot save probe failed' })
  expect(withReason).toContain('Persistent cache unavailable')
  expect(withReason).toContain('slot save probe failed')
  // Ordinary forwarding is the truthful promise for this state.
  expect(withReason).toContain('Requests run normally without cache coordination.')

  const withoutReason = panel({ requested: true, status: 'unsupported' })
  expect(withoutReason).toContain('The capability probe failed.')
})

test('panel error names the safety failure without promising normal requests', () => {
  const reason = 'failed to clear the slot after a failed restore'
  const withReason = panel({ requested: true, status: 'error', reason })
  expect(withReason).toContain('Prompt cache stopped after a safety failure')
  expect(withReason).toContain(reason)
  // The misleading reassurance from issue #21 must never appear for errors.
  expect(withReason).not.toContain('run normally')
  // The rejection risk and the manual recovery action are stated.
  expect(withReason).toContain('Requests may be rejected')
  expect(withReason).toContain('Restart this server')

  const withoutReason = panel({ requested: true, status: 'error' })
  expect(withoutReason).toContain('The cache could not put the slot in a known state.')
  expect(withoutReason).not.toContain('run normally')
})
