import { useEffect } from 'react'
import { Button } from './Button'

// 遮罩 + 白卡 18px：规范无投影，层次靠遮罩与 hairline。
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
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-ink/30 p-6"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
      role="dialog"
      aria-modal="true"
    >
      <div
        className={`max-h-[85dvh] ${width} max-w-full overflow-y-auto rounded-card border border-hairline bg-canvas`}
      >
        <div className="flex items-center justify-between gap-4 border-b border-divider px-6 py-4">
          <h2 className="text-[16px] font-semibold text-ink">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            className="press focus-ring flex size-7 items-center justify-center rounded-full text-ink-48 hover:bg-pearl hover:text-ink"
            aria-label="关闭"
          >
            ✕
          </button>
        </div>
        <div className="px-6 py-5">{children}</div>
        {footer && (
          <div className="flex items-center justify-end gap-3 border-t border-divider px-6 py-4">
            {footer}
          </div>
        )}
      </div>
    </div>
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
