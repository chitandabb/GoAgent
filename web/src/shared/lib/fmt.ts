const pad = (n: number) => String(n).padStart(2, '0')

export function fmtDateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  const y = d.getFullYear() === new Date().getFullYear() ? '' : `${d.getFullYear()}-`
  return `${y}${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function fmtClock(iso: string): string {
  const d = new Date(iso)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

export function shortId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 10)}…` : id
}
