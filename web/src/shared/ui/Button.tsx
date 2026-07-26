import type { ButtonHTMLAttributes } from 'react'

type Variant = 'primary' | 'ghost' | 'neutral' | 'dark' | 'danger-ghost'
type Size = 'md' | 'sm'

// 圆角语法：pill = 操作信号；dark utility 用 8px。规范：按压态 scale(0.95)。
const variantCls: Record<Variant, string> = {
  primary: 'rounded-full bg-primary text-white hover:bg-primary-focus',
  ghost: 'rounded-full border border-primary bg-transparent text-primary hover:bg-primary/5',
  neutral: 'rounded-full border border-hairline bg-pearl text-ink-80 hover:bg-parchment',
  dark: 'rounded-utility bg-ink text-white hover:bg-ink-80',
  'danger-ghost': 'rounded-full border border-danger bg-transparent text-danger hover:bg-danger/5',
}

const sizeCls: Record<Size, string> = {
  md: 'h-10 px-[22px] text-[14px]',
  sm: 'h-8 px-4 text-[13px]',
}

interface Props extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: Size
}

export function Button({
  variant = 'primary',
  size = 'md',
  className = '',
  type = 'button',
  ...rest
}: Props) {
  return (
    <button
      type={type}
      className={`press focus-ring inline-flex items-center justify-center gap-1.5 font-normal disabled:pointer-events-none disabled:opacity-45 ${variantCls[variant]} ${sizeCls[size]} ${className}`}
      {...rest}
    />
  )
}
