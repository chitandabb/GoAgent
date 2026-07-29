import type { Tone } from '@/shared/lib/status'
import { cva } from 'class-variance-authority'
import { cn } from '@/shared/lib/utils'

const badgeVariants = cva(
  'inline-flex items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-0.5 text-[12px] font-semibold',
  {
    variants: {
      tone: {
        gray: 'bg-neutral-soft text-neutral-muted',
        blue: 'bg-info-soft text-primary',
        green: 'bg-ok-soft text-ok',
        orange: 'bg-warn-soft text-warn',
        red: 'bg-danger-soft text-danger',
      } satisfies Record<Tone, string>,
    },
  },
)

export function Badge({
  tone,
  children,
  dot = false,
  className,
}: {
  tone: Tone
  children: React.ReactNode
  dot?: boolean
  className?: string
}) {
  return (
    <span className={cn(badgeVariants({ tone }), className)}>
      {dot && <i className="size-1.5 rounded-full bg-current opacity-70" />}
      {children}
    </span>
  )
}
