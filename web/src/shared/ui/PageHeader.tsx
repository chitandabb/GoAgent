// 页面标题：display 层级，中文标题不用负字距（负字距只适合拉丁字形）。
export function PageHeader({
  title,
  subtitle,
  actions,
  eyebrow,
}: {
  title: React.ReactNode
  subtitle?: React.ReactNode
  actions?: React.ReactNode
  eyebrow?: React.ReactNode
}) {
  return (
    <header className="mb-8 flex flex-wrap items-end justify-between gap-4">
      <div>
        {eyebrow && (
          <div className="mb-1 text-[13px] font-semibold text-ink-48">{eyebrow}</div>
        )}
        <h1 className="text-[28px] font-semibold leading-[1.15] text-ink">{title}</h1>
        {subtitle && <p className="mt-2 text-[14px] text-ink-48">{subtitle}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-3">{actions}</div>}
    </header>
  )
}
