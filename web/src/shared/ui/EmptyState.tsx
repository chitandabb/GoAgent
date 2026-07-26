export function EmptyState({
  title,
  description,
  action,
}: {
  title: string
  description?: string
  action?: React.ReactNode
}) {
  return (
    <div className="flex flex-col items-center gap-2 px-6 py-16 text-center">
      <p className="text-[15px] font-semibold text-ink-80">{title}</p>
      {description && <p className="max-w-sm text-[13px] text-ink-48">{description}</p>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  )
}
