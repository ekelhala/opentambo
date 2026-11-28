import { InfoIcon } from '@/components/icons'
import type { State } from '@/lib/api'

/**
 * The gateway surface: routes derived from what is actually serving right now.
 * No invented traffic numbers — the routes and aliases are the facts.
 */
export function GatewayView({ state }: { state: State }) {
  const serving = state.runs.filter((r) => r.status === 'running' || r.status === 'stopping')
  return (
    <div>
      <h2 className="pane-title">Gateway</h2>
      <p className="pane-lede">OpenAI-compatible traffic on this listener, routed by the request's model field.</p>

      <div className="panel">
        <div className="panel-head">
          <span className="panel-title">Routes</span>
          <span className="panel-meta">{serving.length ? `${serving.length} served` : 'nothing running'}</span>
        </div>
        <div className="panel-body grid grid-cols-[minmax(0,1fr)] gap-3.5">
          <div className="route">
            <span className="method">GET</span> /v1/models <span className="arrow">→</span> live inventory
            {serving.length ? (
              <span className="badge badge-ok">{serving.length} served</span>
            ) : (
              <span className="badge badge-idle">empty list</span>
            )}
          </div>
          {serving.length ? (
            serving.map((run) => (
              <div
                key={run.id}
                className="grid grid-cols-[minmax(0,1fr)] gap-1.5 border-t border-[var(--t-line-soft)] pt-3.5"
              >
                <div className="route">
                  <span className="method">POST</span> /v1/chat/completions <span className="arrow">→</span>{' '}
                  <span className="text-[var(--t-text)]">"{run.launch.name}"</span> <span className="arrow">→</span>{' '}
                  127.0.0.1:{run.launch.port}
                </div>
                <div className="route pl-1">
                  <span className="method">POST</span> /v1/completions · /v1/embeddings · /v1/responses{' '}
                  <span className="arrow">→</span> same alias, same port
                </div>
              </div>
            ))
          ) : (
            <p className="border-t border-[var(--t-line-soft)] pt-3.5 text-[13px] text-[var(--t-text-2)]">
              Nothing is serving yet. Launch a server and its API model name becomes a stable route here and at your
              LiteLLM gateway.
            </p>
          )}
        </div>
      </div>

      <div className="callout callout-info mt-5" style={{ maxWidth: '46rem' }}>
        <InfoIcon />
        <p>
          SSE streams and error statuses pass through untouched. A caller disconnecting cancels the upstream request.
          Auth headers are stripped before forwarding.
        </p>
      </div>
    </div>
  )
}
