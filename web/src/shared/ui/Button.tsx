import type { ButtonHTMLAttributes } from 'react'
import { Slot } from '@radix-ui/react-slot'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/shared/lib/utils'

export const buttonVariants = cva(
  'press focus-ring inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap font-normal disabled:pointer-events-none disabled:opacity-45 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0',
  {
    variants: {
      variant: {
        primary: 'rounded-full bg-primary text-white hover:bg-primary-focus',
        ghost:
          'rounded-full border border-primary bg-transparent text-primary hover:bg-primary/5',
        neutral:
          'rounded-full border border-hairline bg-pearl text-ink-80 hover:bg-parchment',
        dark: 'rounded-utility bg-ink text-white hover:bg-ink-80',
        'danger-ghost':
          'rounded-full border border-danger bg-transparent text-danger hover:bg-danger/5',
      },
      size: {
        md: 'h-10 px-[22px] text-[14px]',
        sm: 'h-8 px-4 text-[13px]',
        icon: 'size-10 rounded-full p-0',
      },
    },
    defaultVariants: { variant: 'primary', size: 'md' },
  },
)

interface Props
  extends ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean
}

export function Button({
  variant,
  size,
  className,
  type = 'button',
  asChild = false,
  ...rest
}: Props) {
  const Comp = asChild ? Slot : 'button'
  return (
    <Comp
      type={asChild ? undefined : type}
      className={cn(buttonVariants({ variant, size }), className)}
      {...rest}
    />
  )
}
