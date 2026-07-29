import * as DialogPrimitive from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { cn } from '@/shared/lib/utils'
import { Button } from './Button'

export function Dialog({
  open,
  title,
  onClose,
  children,
  footer,
  width = 'w-[420px]',
}: {
  open: boolean
  title: React.ReactNode
  onClose: () => void
  children: React.ReactNode
  footer?: React.ReactNode
  width?: string
}) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-ink/30 data-[state=closed]:animate-out data-[state=open]:animate-in" />
        <DialogPrimitive.Content
          className={cn(
            'fixed left-1/2 top-1/2 z-50 max-h-[85dvh] max-w-[calc(100%-3rem)] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-card border border-hairline bg-canvas shadow-none focus:outline-none',
            width,
          )}
        >
          <div className="flex items-center justify-between gap-4 border-b border-divider px-6 py-4">
            <DialogPrimitive.Title className="text-[16px] font-semibold text-ink">
              {title}
            </DialogPrimitive.Title>
            <DialogPrimitive.Close
              className="press focus-ring flex size-7 items-center justify-center rounded-full text-ink-48 hover:bg-pearl hover:text-ink"
              aria-label="关闭"
            >
              <X className="size-4" />
            </DialogPrimitive.Close>
          </div>
          <div className="px-6 py-5">{children}</div>
          {footer && (
            <div className="flex items-center justify-end gap-3 border-t border-divider px-6 py-4">
              {footer}
            </div>
          )}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}

export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel = '确认',
  danger = false,
  busy = false,
  onConfirm,
  onCancel,
}: {
  open: boolean
  title: string
  message: React.ReactNode
  confirmLabel?: string
  danger?: boolean
  busy?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Dialog
      open={open}
      title={title}
      onClose={onCancel}
      footer={
        <>
          <Button variant="neutral" onClick={onCancel} disabled={busy}>
            取消
          </Button>
          <Button
            variant={danger ? 'danger-ghost' : 'primary'}
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? '处理中…' : confirmLabel}
          </Button>
        </>
      }
    >
      <p className="text-[14px] leading-[1.7] text-ink-80">{message}</p>
    </Dialog>
  )
}
