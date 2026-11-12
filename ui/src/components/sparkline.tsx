// Small dependency-free area chart, like the mockup's inline SVG.
export function Sparkline({
  values,
  height = 28,
  className,
}: {
  values: number[]
  height?: number
  className?: string
}) {
  if (values.length < 2) return <div style={{ height }} className={className} />
  const width = 120
  const min = Math.min(...values)
  const max = Math.max(...values)
  const span = max - min || 1
  const pad = 2
  const points = values.map((v, i) => [
    (i * width) / (values.length - 1),
    height - pad - ((v - min) / span) * (height - pad * 2),
  ])
  const line = points.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join('')
  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      className={className}
      style={{ width: '100%', height }}
      aria-hidden
    >
      <path d={`${line} L ${width} ${height} L 0 ${height} Z`} fill="currentColor" opacity={0.12} />
      <path d={line} fill="none" stroke="currentColor" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

export function BarChart({ values, height = 190 }: { values: number[]; height?: number }) {
  if (values.length < 2)
    return (
      <div style={{ height }} className="text-muted-foreground grid place-items-center text-xs">
        No samples yet
      </div>
    )
  const width = 640
  const max = Math.max(...values) || 1
  const bw = width / values.length
  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      style={{ width: '100%', height }}
      aria-hidden
      className="text-amber-500"
    >
      {values.map((v, i) => {
        const h = (v / max) * (height - 8)
        return (
          <rect
            key={i}
            x={i * bw + bw * 0.18}
            y={height - h}
            width={bw * 0.64}
            height={Math.max(h, 1)}
            rx={1.5}
            fill="currentColor"
            opacity={0.35 + (0.65 * v) / max}
          />
        )
      })}
    </svg>
  )
}

export function AreaChart({
  values,
  height = 150,
  className = 'text-sky-500',
}: {
  values: number[]
  height?: number
  className?: string
}) {
  if (values.length < 2)
    return (
      <div style={{ height }} className="text-muted-foreground grid place-items-center text-xs">
        No samples yet
      </div>
    )
  const width = 640
  const max = Math.max(...values) || 1
  const points = values.map((v, i) => [
    (i * width) / (values.length - 1),
    height - 4 - (v / max) * (height - 12),
  ])
  const line = points.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join('')
  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      style={{ width: '100%', height }}
      aria-hidden
      className={className}
    >
      {[0.25, 0.5, 0.75].map((f) => (
        <line
          key={f}
          x1={0}
          x2={width}
          y1={height * f}
          y2={height * f}
          stroke="var(--border)"
          vectorEffect="non-scaling-stroke"
        />
      ))}
      <path d={`${line} L ${width} ${height} L 0 ${height} Z`} fill="currentColor" opacity={0.12} />
      <path d={line} fill="none" stroke="currentColor" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
    </svg>
  )
}
