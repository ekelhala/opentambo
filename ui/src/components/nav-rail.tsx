import { navigate, type View } from '@/lib/router'
import { cn } from 'cn'
import {
  DownloadIcon,
  FileTextIcon,
  LayoutDashboardIcon,
  ServerIcon,
} from 'lucide-react'

const ITEMS: { view: View; label: string; icon: typeof ServerIcon }[] = [
  { view: 'dashboard', label: 'Dashboard', icon: LayoutDashboardIcon },
  { view: 'servers', label: 'Servers', icon: ServerIcon },
  { view: 'downloads', label: 'Downloads', icon: DownloadIcon },
  { view: 'files', label: 'Model files', icon: FileTextIcon },
]

export function NavRail({
  active,
  connected,
}: {
  active: View
  connected: boolean
}) {
  return (
    <nav
      aria-label="Main"
      className="bg-background/80 fixed inset-x-0 bottom-0 z-40 flex items-center justify-around border-t px-2 py-1.5 backdrop-blur md:static md:inset-auto md:h-dvh md:w-16 md:shrink-0 md:flex-col md:justify-start md:gap-1 md:border-t-0 md:border-r md:px-0 md:pt-4 md:pb-3"
    >
      {ITEMS.map(({ view, label, icon: Icon }) => {
        const isActive = active === view || (active === 'server' && view === 'servers')
        return (
          <button
            key={view}
            onClick={() => navigate(view)}
            title={label}
            aria-label={label}
            aria-current={isActive ? 'page' : undefined}
            className={cn(
              'relative grid size-11 place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground',
              isActive && 'bg-secondary text-foreground'
            )}
          >
            <Icon className="size-5" />
            {isActive && (
              <span className="bg-foreground absolute hidden rounded-full md:block md:h-5 md:w-[3px] md:-left-2.5" />
            )}
          </button>
        )
      })}
      <span className="md:grow" />
      <span
        role="status"
        aria-label={connected ? 'llama-desk healthy' : 'Disconnected from llama-desk'}
        title={connected ? 'llama-desk healthy' : 'Disconnected'}
        className={cn(
          'absolute hidden size-2 rounded-full md:bottom-8 md:block',
          connected ? 'bg-emerald-500' : 'bg-destructive'
        )}
      />
      <span className="text-muted-foreground absolute bottom-2 left-1/2 hidden -translate-x-1/2 text-[10px] tracking-widest md:block">
        v0.1
      </span>
    </nav>
  )
}
