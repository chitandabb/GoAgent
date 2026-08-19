import { FileText, Plus, RotateCcw } from 'lucide-react'
import type { DataSource, ExternalCase } from '@/shared/api/m1-types'
import { caseStatusMeta, priorityMeta } from '@/shared/lib/status'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'

export function DossierPanel({
  extCase,
  dataSources,
  selectedDataSourceIds,
  pendingCases,
  onDataSourceToggle,
  onOpenCase,
  onNewWorkspace,
}: {
  extCase: ExternalCase
  dataSources: DataSource[]
  selectedDataSourceIds: string[]
  pendingCases: ExternalCase[]
  onDataSourceToggle: (dataSourceId: string) => void
  onOpenCase: (externalCaseId: string) => void
  onNewWorkspace: () => void
}) {
  const otherPendingCases = pendingCases.filter(
    (item) => item.externalCaseId !== extCase.externalCaseId,
  )

  return (
    <div className="flex min-h-full flex-col bg-canvas">
      <div className="border-b border-divider px-5 py-4">
        <div className="mb-2 flex items-center justify-between gap-3">
          <p className="text-[11px] font-semibold text-ink-48">当前卷宗</p>
          <Button size="icon" variant="neutral" className="!size-8" onClick={onNewWorkspace} title="为当前工单新建本地工作区">
            <Plus />
            <span className="sr-only">为当前工单新建本地工作区</span>
          </Button>
        </div>
        <p className="text-[13px] font-semibold text-ink">{extCase.externalCaseKey}</p>
        <h2 className="mt-1 text-[17px] font-semibold leading-[1.35] text-ink">{extCase.title}</h2>
        <div className="mt-3 flex flex-wrap gap-2">
          <Badge tone={caseStatusMeta[extCase.status].tone}>{caseStatusMeta[extCase.status].label}</Badge>
          <Badge tone={priorityMeta[extCase.priority].tone}>优先级 {priorityMeta[extCase.priority].label}</Badge>
        </div>
      </div>

      <div className="flex flex-col gap-6 px-5 py-5">
        <section>
          <div className="mb-2 flex items-center gap-2">
            <FileText className="size-3.5 text-ink-48" />
            <h3 className="text-[12px] font-semibold text-ink">工单摘要</h3>
          </div>
          <p className="text-[12px] leading-[1.65] text-ink-80">{extCase.description}</p>
          <dl className="mt-3 divide-y divide-divider text-[11px]">
            <div className="flex justify-between gap-3 py-2"><dt className="text-ink-48">客户</dt><dd className="text-right text-ink-80">{extCase.customerName || '—'}</dd></div>
            <div className="flex justify-between gap-3 py-2"><dt className="text-ink-48">产品</dt><dd className="text-right text-ink-80">{[extCase.productName, extCase.productVersion].filter(Boolean).join(' ') || '—'}</dd></div>
            <div className="flex justify-between gap-3 py-2"><dt className="text-ink-48">上报</dt><dd className="text-right text-ink-80">{fmtDateTime(extCase.reportedAt)}</dd></div>
          </dl>
        </section>

        <section>
          <h3 className="mb-2 text-[12px] font-semibold text-ink">证据数据源</h3>
          <div className="flex flex-col gap-2">
            {dataSources.map((source) => {
              const selected = selectedDataSourceIds.includes(source.id)
              return (
                <label key={source.id} className="flex items-center gap-3 py-1.5">
                  <input type="checkbox" checked={selected} onChange={() => onDataSourceToggle(source.id)} className="size-4 accent-primary" />
                  <span className="min-w-0 flex-1 truncate text-[12px] text-ink-80">{source.name}</span>
                  <Badge tone={source.status === 'active' ? 'green' : 'gray'}>{source.status === 'active' ? '可用' : '停用'}</Badge>
                </label>
              )
            })}
          </div>
        </section>

        <section>
          <h3 className="mb-2 text-[12px] font-semibold text-ink">工单附件</h3>
          {extCase.attachments.length === 0 ? (
            <p className="text-[11px] text-ink-48">没有附件声明</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {extCase.attachments.map((attachment) => (
                <li key={attachment.externalAttachmentKey} className="rounded-utility bg-pearl px-3 py-2">
                  <p className="break-words text-[11px] font-semibold text-ink-80">{attachment.fileName}</p>
                  <p className="mt-0.5 text-[10px] text-ink-48">{attachment.mediaType} · 仅元数据</p>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="border-t border-divider pt-5">
          <h3 className="mb-2 text-[12px] font-semibold text-ink">待办工单</h3>
          <div className="flex flex-col gap-1">
            {otherPendingCases.slice(0, 4).map((item) => (
              <button key={item.externalCaseId} type="button" onClick={() => onOpenCase(item.externalCaseId)} className="press flex items-start gap-2 rounded-utility px-2 py-2 text-left hover:bg-pearl">
                <RotateCcw className="mt-0.5 size-3 shrink-0 text-ink-48" />
                <span className="min-w-0">
                  <span className="block text-[11px] font-semibold text-ink">{item.externalCaseKey}</span>
                  <span className="line-clamp-2 text-[10px] leading-[1.45] text-ink-48">{item.title}</span>
                </span>
              </button>
            ))}
            {otherPendingCases.length === 0 && <p className="text-[11px] text-ink-48">没有其他待办工单</p>}
          </div>
        </section>
      </div>
    </div>
  )
}
