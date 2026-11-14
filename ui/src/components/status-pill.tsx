import { cn } from 'cn'

const styles: Record<string, string> = {
  running: 'badge-ok',
  completed: 'badge-ok',
  stored: 'badge-ok',
  stopping: 'badge-warn',
  launching: 'badge-warn',
  queued: 'badge-warn',
  interrupted: 'badge-warn',
  downloading: 'badge-info',
  failed: 'badge-bad',
  error: 'badge-bad',
  cancelled: 'badge-idle',
  stopped: 'badge-idle',
}

export function StatusPill({ status, className }: { status: string; className?: string }) {
  return <span className={cn('badge', styles[status] ?? 'badge-idle', className)}>{status}</span>
}
