export function MeterRow({
  label,
  value,
  detail,
  tone = 'accent',
}: {
  label: string
  /** 0–100 */
  value: number
  detail: string
  tone?: 'accent' | 'info' | 'ok' | 'warn'
}) {
  return (
    <div className="grid grid-cols-[92px_1fr_104px] items-center gap-3 py-2">
      <span className="text-[13px] font-medium text-[var(--t-text-2)]">{label}</span>
      <span className={cnProgress(tone)}>
        <i style={{ width: `${Math.min(100, Math.max(0, value))}%` }} />
      </span>
      <span className="num text-right text-xs text-[var(--t-text-3)]">{detail}</span>
    </div>
  )
}

function cnProgress(tone: 'accent' | 'info' | 'ok' | 'warn') {
  const mod = tone === 'accent' ? '' : tone
  return `progress ${mod}`.trim()
}
