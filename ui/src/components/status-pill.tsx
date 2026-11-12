import { Badge } from '@/components/ui/badge'
import { cn } from 'cn'

const styles: Record<string, string> = {
  running: 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400',
  downloading: 'bg-sky-500/15 text-sky-600 dark:text-sky-400',
  completed: 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400',
  stopping: 'bg-amber-500/15 text-amber-600 dark:text-amber-400',
  interrupted: 'bg-amber-500/15 text-amber-600 dark:text-amber-400',
  failed: 'bg-destructive/15 text-destructive',
  cancelled: 'bg-muted text-muted-foreground',
  stopped: 'bg-muted text-muted-foreground',
  queued: 'bg-muted text-muted-foreground',
}

export function StatusPill({ status }: { status: string }) {
  return (
    <Badge variant="secondary" className={cn('gap-1.5 py-0.5', styles[status])}>
      <span
        className={cn(
          'size-1.5 rounded-full bg-current',
          (status === 'running' || status === 'downloading') && 'animate-pulse'
        )}
      />
      {status}
    </Badge>
  )
}
