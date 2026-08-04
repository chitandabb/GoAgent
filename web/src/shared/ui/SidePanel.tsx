import * as DialogPrimitive from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { cn } from '@/shared/lib/utils'

export function SidePanel({
  open,
  title,
  side,
  onClose,
  children,
}: {
  open: boolean
  title: string
  side: 'left' | 'right'
  onClose: () => void
  children: React.ReactNode
}) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-ink/30" />
        <DialogPrimitive.Content
          className={cn(
            'fixed bottom-0 top-0 z-50 flex w-[min(360px,calc(100%-2rem))] flex-col bg-canvas outline-none',
            side === 'left' ? 'left-0 border-r border-hairline' : 'right-0 border-l border-hairline',
          )}
        >
          <div className="flex h-12 shrink-0 items-center justify-between border-b border-divider px-4">
            <DialogPrimitive.Title className="text-[14px] font-semibold text-ink">
              {title}
            </DialogPrimitive.Title>
            <DialogPrimitive.Close
              className="press focus-ring flex size-8 items-center justify-center rounded-full text-ink-48 hover:bg-pearl hover:text-ink"
              aria-label="关闭"
            >
              <X className="size-4" />
            </DialogPrimitive.Close>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}
