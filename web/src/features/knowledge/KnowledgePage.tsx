import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type { KnowledgeDoc } from '@/shared/api'
import { docStatusMeta } from '@/shared/lib/status'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { FilterChips } from '@/shared/ui/Chips'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { PageHeader } from '@/shared/ui/PageHeader'
import { CaseCards } from './CaseCards'

const tabs = [
  { value: 'personal', label: '个人知识库' },
  { value: 'global', label: '全局知识库' },
  { value: 'cases', label: '案例卡片' },
]

function fmtSize(bytes: number): string {
  if (bytes >= 1_048_576) return `${(bytes / 1_048_576).toFixed(1)} MB`
  return `${Math.round(bytes / 1024)} KB`
}

function DocTable({ scope }: { scope: 'personal' | 'global' }) {
  const docs = useQuery({
    queryKey: ['knowledge-docs', scope],
    queryFn: () => api.listKnowledgeDocs(scope),
    // 处理中的文档轮询刷新状态（processing → ready）
    refetchInterval: (q) =>
      (q.state.data ?? []).some((d) => d.status === 'processing') ? 1500 : false,
  })

  const columns: Column<KnowledgeDoc>[] = [
    {
      key: 'title',
      title: '文档',
      className: 'max-w-[300px]',
      render: (d) => (
        <div>
          <p className="line-clamp-1 font-semibold text-ink">{d.title}</p>
          {d.failReason && (
            <p className="mt-0.5 line-clamp-1 text-[12px] text-danger">{d.failReason}</p>
          )}
        </div>
      ),
    },
    {
      key: 'type',
      title: '类型',
      render: (d) => <span className="text-ink-48">{d.fileType}</span>,
    },
    { key: 'size', title: '大小', render: (d) => fmtSize(d.sizeBytes) },
    {
      key: 'status',
      title: '状态',
      render: (d) => (
        <Badge tone={docStatusMeta[d.status].tone} dot={d.status === 'processing'}>
          {docStatusMeta[d.status].label}
        </Badge>
      ),
    },
    {
      key: 'chunks',
      title: '检索块',
      render: (d) =>
        d.status === 'ready' ? (
          <div>
            <span className="tabular-nums">{d.chunkCount}</span>
            {d.elementSummary && (
              <p className="mt-0.5 text-[11px] text-ink-48">{d.elementSummary}</p>
            )}
          </div>
        ) : (
          <span className="text-ink-48">—</span>
        ),
    },
    { key: 'owner', title: scope === 'global' ? '维护者' : '所有者', render: (d) => d.owner },
    {
      key: 'updatedAt',
      title: '更新时间',
      className: 'text-ink-48',
      render: (d) => fmtDateTime(d.updatedAt),
    },
  ]

  return (
    <DataTable
      columns={columns}
      rows={docs.data ?? []}
      rowKey={(d) => d.documentId}
      loading={docs.isPending}
      emptyText={scope === 'personal' ? '个人知识库还没有文档' : '全局知识库暂无文档'}
      emptyDescription={
        scope === 'personal'
          ? '上传实施笔记、交接文档或现场记录，解析完成后即可在知识助手中被检索；文档仅自己可见。'
          : '全局知识库由管理员导入正式产品资料与已确认案例。'
      }
    />
  )
}

export function KnowledgePage() {
  const { user } = useAuth()
  const qc = useQueryClient()
  const [tab, setTab] = useState('personal')

  const upload = useMutation({
    mutationFn: (scope: 'personal' | 'global') => api.uploadKnowledgeDoc(scope),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['knowledge-docs'] }),
  })

  const canUploadHere =
    tab === 'personal' || (tab === 'global' && user?.role === 'admin')

  return (
    <div>
      <PageHeader
        title="知识库"
        subtitle="个人知识库仅自己可见；全局知识库由管理员维护；案例卡片是人工确认后的标准处理方案"
        actions={
          canUploadHere ? (
            <Button
              onClick={() => upload.mutate(tab as 'personal' | 'global')}
              disabled={upload.isPending}
            >
              {upload.isPending ? '上传中…' : '上传文档（演示）'}
            </Button>
          ) : undefined
        }
      />

      <div className="mb-5 flex items-center justify-between gap-4">
        <FilterChips options={tabs} value={tab} onChange={setTab} />
        {tab === 'global' && user?.role !== 'admin' && (
          <span className="text-[12px] text-ink-48">
            全局知识库对 analyst 只读；导入与审核由管理员执行
          </span>
        )}
        {tab === 'personal' && (
          <span className="text-[12px] text-ink-48">
            会话临时文件在知识助手中上传，选择“仅本次使用”即不进入知识库
          </span>
        )}
      </div>

      {tab === 'cases' ? (
        <CaseCards />
      ) : (
        <>
          {tab === 'personal' && (
            <Card className="mb-4 bg-pearl px-5 py-3.5">
              <p className="text-[12px] leading-[1.7] text-ink-48">
                上传后文档进入异步解析（文本/表格/截图分流、OCR 与多模态描述），状态变为“可检索”前不参与检索；解析失败会明确标记，不会伪装成可检索成功。个人文档不会因被引用而公开。
              </p>
            </Card>
          )}
          <DocTable scope={tab as 'personal' | 'global'} />
        </>
      )}
    </div>
  )
}
