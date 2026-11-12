export function bytes(n: number): string {
  if (n < 0) return '?'
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)} GB`
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)} MB`
  if (n >= 1e3) return `${Math.round(n / 1e3)} kB`
  return `${n} B`
}

export function duration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)} s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} m ${String(Math.round(seconds % 60)).padStart(2, '0')} s`
  const hours = Math.floor(minutes / 60)
  return `${hours} h ${String(minutes % 60).padStart(2, '0')} m`
}

export function uptime(started: string, ended?: string): string {
  const end = ended ? new Date(ended).getTime() : Date.now()
  return duration(Math.max(0, (end - new Date(started).getTime()) / 1000))
}

export function date(value: string): string {
  return new Date(value).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function shellQuote(arg: string): string {
  return /^[\w./:=@+-]+$/.test(arg) ? arg : `'${arg.replaceAll("'", `'\\''`)}'`
}
