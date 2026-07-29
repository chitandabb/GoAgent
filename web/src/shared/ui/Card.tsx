import type { HTMLAttributes } from 'react'
import { cn } from '@/shared/lib/utils'

export function Card({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="card"
      className={cn('rounded-card border border-hairline bg-canvas shadow-none', className)}
      {...props}
    />
  )
}

export function CardTitle({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <h3 data-slot="card-title" className={cn('text-[14px] font-semibold text-ink', className)}>
      {children}
    </h3>
  )
}
