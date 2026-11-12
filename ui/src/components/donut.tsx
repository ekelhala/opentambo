// Dependency-free SVG donut, like the mockup's VRAM ring.
export interface DonutSegment {
  label: string
  value: number
  color: string
}

export function Donut({
  segments,
  centerValue,
  centerLabel,
  size = 132,
}: {
  segments: DonutSegment[]
  centerValue: string
  centerLabel: string
  size?: number
}) {
  const total = segments.reduce((sum, s) => sum + s.value, 0) || 1
  const r = 15.915
  let offset = 25
  return (
    <svg width={size} height={size} viewBox="0 0 42 42" aria-hidden>
      <circle cx="21" cy="21" r={r} fill="none" stroke="var(--secondary)" strokeWidth="4.4" />
      {segments.map((s) => {
        const frac = (s.value / total) * 100
        offset -= frac
        return (
          <circle
            key={s.label}
            cx="21"
            cy="21"
            r={r}
            fill="none"
            stroke={s.color}
            strokeWidth="4.4"
            strokeDasharray={`${frac} ${100 - frac}`}
            strokeDashoffset={offset}
            strokeLinecap="round"
          />
        )
      })}
      <text
        x="21"
        y="20.4"
        textAnchor="middle"
        fontSize="6.2"
        fontWeight="700"
        fill="var(--foreground)"
      >
        {centerValue}
      </text>
      <text x="21" y="25.6" textAnchor="middle" fontSize="3.2" fill="var(--muted-foreground)">
        {centerLabel}
      </text>
    </svg>
  )
}
