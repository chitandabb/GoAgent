// 词标 lockup:MES 与 Guard 双色。Guard 承载唯一的交互蓝 ——
// 亮底用 Action Blue，暗底用 Sky Link Blue（DESIGN-apple.md 的暗面规则）。
export function Wordmark({
  on = 'light',
  className = '',
}: {
  on?: 'light' | 'dark'
  className?: string
}) {
  return (
    <span className={`font-semibold tracking-[-0.2px] ${className}`}>
      <span className={on === 'dark' ? 'text-white' : 'text-ink'}>MES</span>
      <span className={on === 'dark' ? 'text-primary-on-dark' : 'text-primary'}>
        Guard
      </span>
    </span>
  )
}
