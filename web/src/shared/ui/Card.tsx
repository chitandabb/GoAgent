// 规范：白卡片 + 1px hairline + 18px 圆角，无投影。层次靠表面色，不靠阴影。
export function Card({
  className = '',
  children,
}: {
  className?: string
  children: React.ReactNode
}) {
  return (
    <div className={`rounded-card border border-hairline bg-canvas ${className}`}>
      {children}
    </div>
  )
}

export function CardTitle({
  children,
  className = '',
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <h3 className={`text-[14px] font-semibold text-ink ${className}`}>{children}</h3>
  )
}
