import { useState } from 'react'
import { Dialog } from './Dialog'

// 演示占位预览：真实实现改为 GET /attachments/{id}/content 的流式代理
// （图片/PDF 走 Range 请求，由 API 校验权限后返回，浏览器不接触 MinIO）。

function isImage(name: string): boolean {
  return /\.(png|jpe?g|webp)$/i.test(name)
}
function isText(name: string): boolean {
  return /\.(log|txt)$/i.test(name)
}

function ImagePlaceholder({ name }: { name: string }) {
  return (
    <svg viewBox="0 0 640 400" className="w-full rounded-utility border border-hairline">
      <rect width="640" height="400" fill="#f5f5f7" />
      <rect x="0" y="0" width="640" height="36" fill="#e8e8ed" />
      <circle cx="18" cy="18" r="5" fill="#d2d2d7" />
      <circle cx="36" cy="18" r="5" fill="#d2d2d7" />
      <rect x="56" y="12" width="220" height="12" rx="6" fill="#d2d2d7" />
      <rect x="24" y="60" width="592" height="52" rx="8" fill="#ffffff" />
      <rect x="40" y="76" width="180" height="14" rx="7" fill="#e0e0e0" />
      <rect x="24" y="128" width="360" height="180" rx="8" fill="#ffffff" />
      <rect x="40" y="148" width="140" height="12" rx="6" fill="#e0e0e0" />
      <rect x="40" y="172" width="300" height="10" rx="5" fill="#ececf0" />
      <rect x="40" y="192" width="300" height="10" rx="5" fill="#ececf0" />
      <rect x="40" y="212" width="220" height="10" rx="5" fill="#ececf0" />
      <rect x="40" y="248" width="90" height="28" rx="14" fill="#0066cc" opacity="0.85" />
      <rect x="400" y="128" width="216" height="180" rx="8" fill="#ffffff" />
      <rect x="416" y="148" width="120" height="12" rx="6" fill="#e0e0e0" />
      <rect x="416" y="172" width="184" height="88" rx="6" fill="#f0f0f0" />
      <text x="320" y="370" textAnchor="middle" fontSize="13" fill="#7a7a7a">
        {name}（演示占位截图）
      </text>
    </svg>
  )
}

const demoLogLines = [
  '2026-07-26 08:12:04.113 INFO  ReportService - report submitted, wo=WO-****, op=OP50',
  '2026-07-26 08:12:04.118 INFO  ReportService - report record created, rows=1',
  '2026-07-26 08:12:04.121 WARN  InventoryLink - lot mapping missing, lot_id=<empty>',
  '2026-07-26 08:12:04.125 INFO  InventoryLink - txn queued to pending list',
  '2026-07-26 08:12:04.130 INFO  ReportService - response ok (elapsed 17ms)',
]

function TextPlaceholder({ name }: { name: string }) {
  return (
    <div className="rounded-utility border border-hairline bg-[#1d1d1f] p-4">
      <p className="mb-2 text-[11px] text-white/40">{name}（演示片段，已脱敏）</p>
      <pre className="overflow-x-auto text-[11px] leading-[1.8] text-[#c9c9ce]">
        {demoLogLines.join('\n')}
      </pre>
    </div>
  )
}

function PdfPlaceholder({ name }: { name: string }) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-utility border border-hairline bg-pearl py-10">
      <svg viewBox="0 0 48 60" className="w-14">
        <rect width="48" height="60" rx="4" fill="#ffffff" stroke="#e0e0e0" />
        <rect x="8" y="10" width="32" height="4" rx="2" fill="#e0e0e0" />
        <rect x="8" y="20" width="32" height="3" rx="1.5" fill="#ececf0" />
        <rect x="8" y="27" width="32" height="3" rx="1.5" fill="#ececf0" />
        <rect x="8" y="34" width="20" height="3" rx="1.5" fill="#ececf0" />
        <text x="24" y="52" textAnchor="middle" fontSize="9" fill="#d70015">
          PDF
        </text>
      </svg>
      <p className="text-[12px] text-ink-48">{name}（演示占位，支持分页与 Range 预览）</p>
    </div>
  )
}

export function AttachmentPreviewDialog({
  name,
  onClose,
}: {
  name: string | null
  onClose: () => void
}) {
  return (
    <Dialog
      open={name !== null}
      title={name ?? ''}
      onClose={onClose}
      width="w-[680px]"
    >
      {name !== null && (
        <>
          {isImage(name) ? (
            <ImagePlaceholder name={name} />
          ) : isText(name) ? (
            <TextPlaceholder name={name} />
          ) : (
            <PdfPlaceholder name={name} />
          )}
          <p className="mt-3 text-[12px] text-ink-48">
            附件保持私有，由 API 校验所有权与任务关联后流式返回；页面不接触对象存储凭证。
          </p>
        </>
      )}
    </Dialog>
  )
}

/** 可点击的附件名：点击打开预览。配合本地 state 使用。 */
export function useAttachmentPreview() {
  const [previewName, setPreviewName] = useState<string | null>(null)
  return {
    previewName,
    openPreview: setPreviewName,
    closePreview: () => setPreviewName(null),
  }
}

export function AttachmentName({
  name,
  onOpen,
  className = '',
}: {
  name: string
  onOpen: (name: string) => void
  className?: string
}) {
  return (
    <button
      type="button"
      onClick={() => onOpen(name)}
      className={`press text-left text-primary hover:underline ${className}`}
    >
      {name}
    </button>
  )
}
