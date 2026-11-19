// Dependency-free SVG charts, drawn in the tambo stroke language:
// one accent hairline, hairline grid, no gradients.

export function Sparkline({ values, height = 28 }: { values: number[]; height?: number }) {
  if (values.length < 2) return <div style={{ height }} />
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
      className="block w-full"
      style={{ height }}
      aria-hidden
    >
      <path d={`${line} L ${width} ${height} L 0 ${height} Z`} className="chart-area" />
      <path d={line} className="chart-line" />
    </svg>
  )
}

export function BarChart({ values, height = 190 }: { values: number[]; height?: number }) {
  if (values.length < 2)
    return (
      <div style={{ height }} className="grid place-items-center text-xs text-[var(--t-text-3)]">
        collecting samples…
      </div>
    )
  const width = 640
  const max = Math.max(...values) || 1
  const bw = width / values.length
  return (
    <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" style={{ width: '100%', height }} aria-hidden>
      {values.map((v, i) => {
        const h = (v / max) * (height - 8)
        return (
          <rect
            key={i}
            x={i * bw + bw * 0.18}
            y={height - h}
            width={bw * 0.64}
            height={Math.max(h, 1)}
            className="chart-bar"
            opacity={0.3 + (0.7 * v) / max}
          />
        )
      })}
    </svg>
  )
}

export function AreaChart({ values, height = 150 }: { values: number[]; height?: number }) {
  if (values.length < 2)
    return (
      <div style={{ height }} className="grid place-items-center text-xs text-[var(--t-text-3)]">
        collecting samples…
      </div>
    )
  const width = 640
  const max = Math.max(...values) || 1
  const points = values.map((v, i) => [(i * width) / (values.length - 1), height - 4 - (v / max) * (height - 12)])
  const line = points.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join('')
  return (
    <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" style={{ width: '100%', height }} aria-hidden>
      {[0.25, 0.5, 0.75].map((f) => (
        <line key={f} x1={0} x2={width} y1={height * f} y2={height * f} className="chart-grid" />
      ))}
      <path d={`${line} L ${width} ${height} L 0 ${height} Z`} className="chart-area" />
      <path d={line} className="chart-line" />
    </svg>
  )
}
