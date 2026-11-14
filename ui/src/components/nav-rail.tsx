import { navigate, type View } from '@/lib/router'
import { cn } from 'cn'
import { DownloadIcon, GatewayIcon, ModelsIcon, RoundsIcon, ServersIcon } from './icons'

const ITEMS: { view: View; label: string; icon: typeof ServersIcon }[] = [
  { view: 'dashboard', label: 'Rounds', icon: RoundsIcon },
  { view: 'servers', label: 'Servers', icon: ServersIcon },
  { view: 'downloads', label: 'Downloads', icon: DownloadIcon },
  { view: 'files', label: 'Models', icon: ModelsIcon },
  { view: 'gateway', label: 'Gateway', icon: GatewayIcon },
]

export function NavRail({
  active,
  connected,
  counts,
}: {
  active: View
  connected: boolean
  counts: Partial<Record<View, number>>
}) {
  return (
    <nav aria-label="Panel sections" className="rail">
      {ITEMS.map(({ view, label, icon: Icon }) => {
        const isActive = active === view || (active === 'server' && view === 'servers')
        return (
          <button
            key={view}
            onClick={() => navigate(view)}
            aria-current={isActive ? 'page' : undefined}
            className="item"
          >
            <Icon />
            {label}
            {counts[view] !== undefined && <span className="count">{counts[view]}</span>}
          </button>
        )
      })}
      <div className={cn('rail-foot', !connected && 'off')} role="status">
        <span className="dot" />
        {connected ? 'API connected' : 'API unreachable'}
      </div>
    </nav>
  )
}
