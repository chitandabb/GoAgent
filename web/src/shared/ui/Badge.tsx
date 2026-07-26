import type { Tone } from '@/shared/lib/status'

const toneCls: Record<Tone, string> = {
  gray: 'bg-[#efeff1] text-[#6e6e73]',
  blue: 'bg-info-soft text-primary',
  green: 'bg-ok-soft text-ok',
  orange: 'bg-warn-soft text-warn',
  red: 'bg-danger-soft text-danger',
}

export function Badge({
  tone,
  children,
  dot = false,
}: {
  tone: Tone
  children: React.ReactNode
  dot?: boolean
}) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-0.5 text-[12px] font-semibold ${toneCls[tone]}`}
    >
      {dot && <i className="size-1.5 rounded-full bg-current opacity-70" />}
      {children}
    </span>
  )
}
