export function MeterRow({
  label,
  value,
  detail,
  className,
}: {
  label: string
  /** 0–100 */
  value: number
  detail: string
  className?: string
}) {
  return (
    <div className="grid grid-cols-[70px_1fr_96px] items-center gap-3 py-1.5">
      <span className="text-muted-foreground text-xs font-medium">{label}</span>
      <span className="bg-secondary h-1.5 overflow-hidden rounded-full">
        <span
          className={`block h-full rounded-full transition-[width] duration-500 ${className ?? 'bg-foreground'}`}
          style={{ width: `${Math.min(100, Math.max(0, value))}%` }}
        />
      </span>
      <span className="text-right text-xs tabular-nums">{detail}</span>
    </div>
  )
}
