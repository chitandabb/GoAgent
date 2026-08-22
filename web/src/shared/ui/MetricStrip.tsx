import { cn } from '@/shared/lib/utils'

export type MetricItem = {
  label: string
  value: React.ReactNode
  detail?: string
  tone?: 'primary' | 'ok' | 'signal' | 'danger'
}

const toneClass = {
  primary: 'bg-primary',
  ok: 'bg-ok',
  signal: 'bg-signal',
  danger: 'bg-danger',
}

export function MetricStrip({ items }: { items: MetricItem[] }) {
  return (
    <div className="mb-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {items.map((item) => (
        <div key={item.label} className="flex items-center gap-3 rounded-card border border-hairline bg-canvas px-4 py-3 shadow-sm shadow-slate-900/5">
          <span className={cn('flex size-2.5 shrink-0 rounded-full', toneClass[item.tone ?? 'primary'])} />
          <div className="min-w-0">
            <div className="flex items-baseline gap-2">
              <span className="text-[20px] font-semibold tabular-nums tracking-[-0.02em] text-ink">{item.value}</span>
              <span className="truncate text-[11px] font-semibold text-ink-48">{item.label}</span>
            </div>
            {item.detail && <p className="mt-0.5 truncate text-[10px] text-ink-48">{item.detail}</p>}
          </div>
        </div>
      ))}
    </div>
  )
}
