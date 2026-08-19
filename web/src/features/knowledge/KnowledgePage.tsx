import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, FileText, RotateCcw, Upload } from 'lucide-react'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type {
  IngestionStage,
  IngestionTaskStatus,
  KnowledgeDocumentListItem,
} from '@/shared/api/m1-types'
import type { Tone } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { knowledgeDocumentFileAccept, knowledgeDocumentFormatLabel } from '@/shared/lib/knowledge-upload'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { ConfirmDialog } from '@/shared/ui/Dialog'
import { EmptyState } from '@/shared/ui/EmptyState'
import { FieldLabel, TextInput } from '@/shared/ui/Field'
import { PageHeader } from '@/shared/ui/PageHeader'
import { Spinner } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'

const terminalStatuses: IngestionTaskStatus[] = ['succeeded', 'partial_succeeded', 'failed', 'cancelled']

const statusMeta: Record<IngestionTaskStatus, { label: string; tone: Tone }> = {
  pending: { label: '等待中', tone: 'gray' },
  running: { label: '解析中', tone: 'blue' },
  retry_wait: { label: '等待重试', tone: 'orange' },
  cancel_requested: { label: '取消中', tone: 'orange' },
  succeeded: { label: '已完成', tone: 'green' },
  partial_succeeded: { label: '部分完成', tone: 'orange' },
  failed: { label: '失败', tone: 'red' },
  cancelled: { label: '已取消', tone: 'orange' },
}

const stageLabels: Record<IngestionStage, string> = {
  uploaded: '文件已暂存',
  scanning: '校验扫描',
  parsing: '解析内容',
  chunking: '生成检索块',
  indexing: '建立索引',
  publishing: '发布知识',
  completed: '处理完成',
}

function statusBadgeTone(status: IngestionTaskStatus | null): Tone {
  if (!status || !(status in statusMeta)) return 'gray'
  return statusMeta[status].tone
}

function statusLabel(status: IngestionTaskStatus | null): string {
  if (!status) return '暂无解析任务'
  return status in statusMeta ? statusMeta[status].label : status
}

function DocumentRow({
  item,
  cancelling,
  onCancel,
  onUploadVersion,
  versionUploading,
}: {
  item: KnowledgeDocumentListItem
  cancelling: boolean
  onCancel: (taskId: string) => void
  onUploadVersion: (item: KnowledgeDocumentListItem) => void
  versionUploading: boolean
}) {
  const [detailOpen, setDetailOpen] = useState(false)
  const active = item.status !== null && !terminalStatuses.includes(item.status)
  const progress = Math.max(0, Math.min(100, item.progressPercent))
  // 展开失败详情时才拉取任务事实（含 attempt 与安全错误摘要）
  const detail = useQuery({
    queryKey: ['ingestion-task', item.taskId],
    queryFn: () => api.getKnowledgeIngestionTask(item.taskId),
    enabled: detailOpen && item.taskId !== '',
  })

  return (
    <Card className="px-5 py-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <div className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-utility bg-pearl text-primary">
            <FileText className="size-4" />
          </div>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <p className="truncate font-semibold text-ink">{item.title || '未命名文档'}</p>
              <Badge tone="gray">v{item.version}</Badge>
            </div>
            <p className="mt-0.5 text-[12px] text-ink-48">
              创建于 {fmtDateTime(item.createdAt)}
              {item.stage ? ` · ${stageLabels[item.stage] ?? item.stage}` : ''}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone={statusBadgeTone(item.status)} dot={active}>
            {statusLabel(item.status)}
          </Badge>
          {active && item.status !== 'cancel_requested' && (
            <Button variant="danger-ghost" size="sm" disabled={cancelling} onClick={() => onCancel(item.taskId)}>
              {cancelling ? '取消中…' : '取消解析'}
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setDetailOpen((current) => !current)}
            aria-expanded={detailOpen}
          >
            <ChevronDown className={`size-3.5 transition-transform ${detailOpen ? 'rotate-180' : ''}`} />
            详情
          </Button>
          <Button
            variant="neutral"
            size="sm"
            disabled={versionUploading}
            onClick={() => onUploadVersion(item)}
          >
            <RotateCcw className="size-3.5" />
            上传新版本
          </Button>
        </div>
      </div>

      {active && (
        <div className="mt-4">
          <div className="mb-1.5 flex items-center justify-between text-[12px] text-ink-48">
            <span>{stageLabels[item.stage ?? 'uploaded']}</span>
            <span className="tabular-nums">{progress}%</span>
          </div>
          <div className="h-1.5 overflow-hidden rounded-full bg-pearl">
            <div className="h-full rounded-full bg-primary transition-[width]" style={{ width: `${progress}%` }} />
          </div>
        </div>
      )}

      {detailOpen && (
        <div className="mt-4 rounded-utility bg-pearl px-4 py-3 text-[12px] leading-[1.7] text-ink-80">
          {detail.isPending ? (
            <span className="text-ink-48">正在读取任务事实…</span>
          ) : detail.isError ? (
            <span className="text-ink-48">
              任务详情读取失败
              <button
                type="button"
                className="press ml-2 text-primary hover:underline"
                onClick={() => void detail.refetch()}
              >
                重试
              </button>
            </span>
          ) : (
            <>
              <p>
                任务 {shortId(detail.data.taskId)} · 已尝试 {detail.data.attemptCount}/
                {detail.data.maxAttempts} 次
                {detail.data.updatedAt ? ` · 更新于 ${fmtDateTime(detail.data.updatedAt)}` : ''}
              </p>
              {detail.data.lastError && (
                <p className="mt-1 text-danger">
                  失败原因（{detail.data.lastError.code}）：{detail.data.lastError.message}
                </p>
              )}
              {!detail.data.lastError && <p className="mt-1 text-ink-48">该任务没有记录错误摘要。</p>}
            </>
          )}
        </div>
      )}
    </Card>
  )
}

export function KnowledgePage() {
  const { user } = useAuth()
  const toast = useToast()
  const queryClient = useQueryClient()
  const isAdmin = user?.role === 'admin'
  const [file, setFile] = useState<File | null>(null)
  const [title, setTitle] = useState('')
  const [page, setPage] = useState(1)
  const [cancelTaskId, setCancelTaskId] = useState<string | null>(null)
  const [versionTarget, setVersionTarget] = useState<KnowledgeDocumentListItem | null>(null)
  const versionFileRef = useRef<HTMLInputElement>(null)

  const documents = useQuery({
    queryKey: ['knowledge-documents', page],
    queryFn: () => api.listKnowledgeDocuments(page, 20),
    enabled: isAdmin,
    refetchInterval: (query) => {
      const items = query.state.data?.items ?? []
      return items.some((item) => item.status !== null && !terminalStatuses.includes(item.status))
        ? 2000
        : false
    },
  })

  const upload = useMutation({
    mutationFn: ({ selectedFile, documentTitle }: { selectedFile: File; documentTitle: string }) =>
      api.createKnowledgeDocument(selectedFile, documentTitle, api.createIdempotencyKey()),
    onSuccess: (response) => {
      setFile(null)
      setTitle('')
      setPage(1)
      void queryClient.invalidateQueries({ queryKey: ['knowledge-documents'] })
      toast.success(response.replayed ? '已恢复相同上传任务' : '上传成功，服务端正在解析')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '上传失败'),
  })

  const uploadVersion = useMutation({
    mutationFn: ({ documentId, selectedFile }: { documentId: string; selectedFile: File }) =>
      api.createKnowledgeDocumentVersion(documentId, selectedFile, api.createIdempotencyKey()),
    onSuccess: (response) => {
      setVersionTarget(null)
      void queryClient.invalidateQueries({ queryKey: ['knowledge-documents'] })
      toast.success(response.replayed ? '已恢复相同版本任务' : '新版本已上传，服务端正在解析')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '上传新版本失败'),
  })

  const cancel = useMutation({
    mutationFn: (taskId: string) => api.cancelKnowledgeIngestionTask(taskId),
    onSuccess: () => {
      setCancelTaskId(null)
      void queryClient.invalidateQueries({ queryKey: ['knowledge-documents'] })
      toast.success('已提交取消请求')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '取消失败'),
  })

  const submitUpload = () => {
    if (!file) {
      toast.error('请先选择要上传的文件')
      return
    }
    upload.mutate({ selectedFile: file, documentTitle: title })
  }

  const chooseVersionFile = (item: KnowledgeDocumentListItem) => {
    setVersionTarget(item)
    versionFileRef.current?.click()
  }

  const onVersionFileChosen = (files: FileList | null) => {
    const selected = files?.[0] ?? null
    if (versionTarget && selected) {
      uploadVersion.mutate({ documentId: versionTarget.documentId, selectedFile: selected })
    } else {
      setVersionTarget(null)
    }
  }

  const items = documents.data?.items ?? []
  const total = documents.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / 20))

  return (
    <div>
      <PageHeader
        title="企业知识库"
        subtitle="上传正式资料并跟踪服务端解析、切块、向量化与发布进度"
      />

      {!isAdmin && (
        <Card className="mb-5 border-warn bg-warn-soft px-5 py-3.5">
          <p className="text-[13px] font-semibold text-warn">仅管理员可管理企业知识库</p>
          <p className="mt-1 text-[12px] text-ink-48">
            当前账号无法上传文档、查看解析任务或取消解析；知识助手仍可检索已发布的企业知识。
          </p>
        </Card>
      )}

      <Card className="mb-6 p-5">
        <div className="mb-4">
          <h2 className="text-[15px] font-semibold text-ink">上传企业文档</h2>
          <p className="mt-1 text-[12px] leading-[1.6] text-ink-48">
            支持 {knowledgeDocumentFormatLabel}。服务端会校验文件签名；超出大小限制会由服务端拒绝。
          </p>
        </div>
        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end">
          <div>
            <FieldLabel htmlFor="knowledge-file">文件</FieldLabel>
            <input
              id="knowledge-file"
              type="file"
              accept={knowledgeDocumentFileAccept}
              disabled={!isAdmin || upload.isPending}
              onChange={(event) => setFile(event.target.files?.[0] ?? null)}
              className="focus-ring block h-10 w-full rounded-[12px] border border-hairline bg-canvas px-3 py-2 text-[12px] text-ink file:mr-3 file:border-0 file:bg-transparent file:text-[12px] file:font-semibold file:text-primary disabled:cursor-not-allowed disabled:opacity-45"
            />
          </div>
          <div>
            <FieldLabel htmlFor="knowledge-title" hint="可选">文档标题</FieldLabel>
            <TextInput
              id="knowledge-title"
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              placeholder={file?.name ?? '留空则由服务端处理'}
              disabled={!isAdmin || upload.isPending}
            />
          </div>
          <Button disabled={!isAdmin || !file || upload.isPending} onClick={submitUpload}>
            <Upload className="size-4" />
            {upload.isPending ? '上传中…' : '上传并解析'}
          </Button>
        </div>
      </Card>

      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-[15px] font-semibold text-ink">文档库</h2>
        {isAdmin && total > 0 && <p className="text-[11px] text-ink-48">共 {total} 个文档</p>}
      </div>

      {!isAdmin ? (
        <EmptyState
          title="文档列表仅管理员可见"
          description="企业知识库的文档清单与解析进度属于管理视角；分析员可在知识助手中检索已发布知识。"
        />
      ) : documents.isPending ? (
        <div className="flex justify-center py-10">
          <Spinner />
        </div>
      ) : documents.isError ? (
        <EmptyState
          title="文档列表读取失败"
          description={documents.error instanceof Error ? documents.error.message : '请稍后重试'}
          action={
            <Button variant="neutral" size="sm" onClick={() => void documents.refetch()}>
              重新加载
            </Button>
          }
        />
      ) : items.length === 0 ? (
        <EmptyState
          title="知识库还没有文档"
          description="上传第一份企业文档后，这里会显示版本与解析状态。"
        />
      ) : (
        <>
          <div className="grid gap-3">
            {items.map((item) => (
              <DocumentRow
                key={item.documentId}
                item={item}
                cancelling={cancel.isPending && cancel.variables === item.taskId}
                onCancel={setCancelTaskId}
                onUploadVersion={chooseVersionFile}
                versionUploading={uploadVersion.isPending && uploadVersion.variables?.documentId === item.documentId}
              />
            ))}
          </div>
          {totalPages > 1 && (
            <div className="mt-4 flex items-center justify-center gap-3 text-[12px] text-ink-48">
              <Button
                variant="neutral"
                size="sm"
                disabled={page <= 1 || documents.isFetching}
                onClick={() => setPage((current) => Math.max(1, current - 1))}
              >
                上一页
              </Button>
              <span className="tabular-nums">
                第 {page} / {totalPages} 页
              </span>
              <Button
                variant="neutral"
                size="sm"
                disabled={page >= totalPages || documents.isFetching}
                onClick={() => setPage((current) => Math.min(totalPages, current + 1))}
              >
                下一页
              </Button>
            </div>
          )}
        </>
      )}

      {/* 为已有文档上传不可变新版本的隐藏入口 */}
      <input
        ref={versionFileRef}
        type="file"
        accept={knowledgeDocumentFileAccept}
        className="hidden"
        onChange={(event) => {
          onVersionFileChosen(event.target.files)
          event.target.value = ''
        }}
      />

      <ConfirmDialog
        open={Boolean(cancelTaskId)}
        title="取消解析"
        message="确认取消这个文档的解析任务？任务会在服务端允许的处理边界停止。"
        confirmLabel="确认取消"
        danger
        busy={cancel.isPending}
        onCancel={() => setCancelTaskId(null)}
        onConfirm={() => cancelTaskId && cancel.mutate(cancelTaskId)}
      />
    </div>
  )
}
