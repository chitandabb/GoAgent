import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { FileText, Upload } from 'lucide-react'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type {
  IngestionStage,
  IngestionTaskStatus,
  KnowledgeIngestionTask,
} from '@/shared/api/m1-types'
import type { Tone } from '@/shared/lib/status'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { ConfirmDialog } from '@/shared/ui/Dialog'
import { EmptyState } from '@/shared/ui/EmptyState'
import { FieldLabel, TextInput } from '@/shared/ui/Field'
import { PageHeader } from '@/shared/ui/PageHeader'
import { useToast } from '@/shared/ui/Toast'

const terminalStatuses: IngestionTaskStatus[] = ['succeeded', 'failed', 'cancelled']

const statusMeta: Record<IngestionTaskStatus, { label: string; tone: Tone }> = {
  pending: { label: '等待中', tone: 'gray' },
  queued: { label: '排队中', tone: 'blue' },
  running: { label: '解析中', tone: 'blue' },
  succeeded: { label: '已完成', tone: 'green' },
  failed: { label: '失败', tone: 'red' },
  cancelled: { label: '已取消', tone: 'orange' },
}

const stageLabels: Record<IngestionStage, string> = {
  staged: '文件已暂存',
  parsing: '解析内容',
  chunking: '生成检索块',
  embedding: '生成向量',
  publishing: '发布知识',
  done: '处理完成',
}

interface UploadRecord {
  taskId: string
  fileName: string
  title: string
  initialTask: KnowledgeIngestionTask
}

function IngestionRecord({
  record,
  onCancel,
  cancelling,
}: {
  record: UploadRecord
  onCancel: (taskId: string) => void
  cancelling: boolean
}) {
  const taskQuery = useQuery({
    queryKey: ['ingestion-task', record.taskId],
    queryFn: () => api.getKnowledgeIngestionTask(record.taskId),
    initialData: record.initialTask,
    refetchInterval: (query) =>
      query.state.data && terminalStatuses.includes(query.state.data.status) ? false : 2000,
  })
  const task = taskQuery.data
  const active = !terminalStatuses.includes(task.status)
  const progress = Math.max(0, Math.min(100, task.progressPercent))

  return (
    <Card className="px-5 py-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <div className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-utility bg-pearl text-primary">
            <FileText className="size-4" />
          </div>
          <div className="min-w-0">
            <p className="truncate font-semibold text-ink">{record.title || record.fileName}</p>
            <p className="mt-0.5 truncate text-[12px] text-ink-48">{record.fileName}</p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone={statusMeta[task.status].tone} dot={active}>
            {statusMeta[task.status].label}
          </Badge>
          {active && (
            <Button
              variant="danger-ghost"
              size="sm"
              disabled={cancelling || Boolean(task.cancelRequestedAt)}
              onClick={() => onCancel(task.taskId)}
            >
              {task.cancelRequestedAt ? '取消中…' : '取消解析'}
            </Button>
          )}
        </div>
      </div>

      <div className="mt-4">
        <div className="mb-1.5 flex items-center justify-between text-[12px] text-ink-48">
          <span>{stageLabels[task.stage]}</span>
          <span className="tabular-nums">{progress}%</span>
        </div>
        <div className="h-1.5 overflow-hidden rounded-full bg-pearl">
          <div
            className={`h-full rounded-full transition-[width] ${task.status === 'failed' ? 'bg-danger' : task.status === 'cancelled' ? 'bg-warn' : 'bg-primary'}`}
            style={{ width: `${progress}%` }}
          />
        </div>
      </div>

      {task.lastError?.message && (
        <p className="mt-3 rounded-utility bg-danger-soft px-3 py-2 text-[12px] text-danger">
          {task.lastError.message}
        </p>
      )}
      <p className="mt-3 text-[11px] text-ink-48">
        创建于 {fmtDateTime(task.createdAt)} · 已尝试 {task.attemptCount}/{task.maxAttempts} 次
        {taskQuery.isError ? ' · 状态刷新失败，将自动重试' : ''}
      </p>
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
  const [records, setRecords] = useState<UploadRecord[]>([])
  const [cancelTaskId, setCancelTaskId] = useState<string | null>(null)

  const upload = useMutation({
    mutationFn: async ({ selectedFile, documentTitle }: { selectedFile: File; documentTitle: string }) => {
      const response = await api.createKnowledgeDocument(
        selectedFile,
        documentTitle,
        api.createIdempotencyKey(),
      )
      return { response, selectedFile, documentTitle }
    },
    onSuccess: ({ response, selectedFile, documentTitle }) => {
      const initialTask: KnowledgeIngestionTask = {
        taskId: response.taskId,
        documentId: response.documentId,
        documentVersionId: response.documentVersionId,
        status: response.status,
        stage: response.stage,
        attemptCount: 0,
        maxAttempts: 0,
        progressPercent: 0,
        createdAt: response.createdAt,
        updatedAt: response.createdAt,
      }
      setRecords((current) => [
        {
          taskId: response.taskId,
          fileName: selectedFile.name,
          title: documentTitle.trim(),
          initialTask,
        },
        ...current.filter((item) => item.taskId !== response.taskId),
      ])
      setFile(null)
      setTitle('')
      toast.success(response.replayed ? '已恢复相同上传任务' : '上传成功，服务端正在解析')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '上传失败'),
  })

  const cancel = useMutation({
    mutationFn: (taskId: string) => api.cancelKnowledgeIngestionTask(taskId),
    onSuccess: (task) => {
      queryClient.setQueryData(['ingestion-task', task.taskId], task)
      setCancelTaskId(null)
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

  return (
    <div>
      <PageHeader
        title="企业知识库"
        subtitle="上传正式资料并跟踪服务端解析、切块、向量化与发布进度"
      />

      {!isAdmin && (
        <Card className="mb-5 border-warn bg-warn-soft px-5 py-3.5">
          <p className="text-[13px] font-semibold text-warn">仅管理员可管理企业知识库</p>
          <p className="mt-1 text-[12px] text-ink-48">当前账号无法上传或取消知识文档解析任务。</p>
        </Card>
      )}

      <Card className="mb-6 p-5">
        <div className="mb-4">
          <h2 className="text-[15px] font-semibold text-ink">上传企业文档</h2>
          <p className="mt-1 text-[12px] leading-[1.6] text-ink-48">
            支持 PDF、PNG、JPEG、文本（含 txt、md 等）和由这些文件组成的 ZIP。服务端会校验文件签名；超出大小限制会由服务端拒绝。
          </p>
        </div>
        <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] md:items-end">
          <div>
            <FieldLabel htmlFor="knowledge-file">文件</FieldLabel>
            <input
              id="knowledge-file"
              type="file"
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
        <h2 className="text-[15px] font-semibold text-ink">本次上传记录</h2>
        <p className="text-[11px] text-ink-48">列表接口尚未提供，当前仅显示本次会话上传记录，刷新页面后会清空。</p>
      </div>

      {records.length === 0 ? (
        <EmptyState
          title="本次会话还没有上传记录"
          description="管理员上传文档后，解析状态与进度会显示在这里。"
        />
      ) : (
        <div className="grid gap-3">
          {records.map((record) => (
            <IngestionRecord
              key={record.taskId}
              record={record}
              cancelling={cancel.isPending && cancel.variables === record.taskId}
              onCancel={setCancelTaskId}
            />
          ))}
        </div>
      )}

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
