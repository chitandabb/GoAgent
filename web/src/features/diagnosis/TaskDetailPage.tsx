import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type { SseConnectionState, TaskEvent, ToolExecution } from '@/shared/api'
import {
  evidenceSourceMeta,
  taskStatusMeta,
  toolStatusMeta,
} from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import {
  AttachmentName,
  AttachmentPreviewDialog,
  useAttachmentPreview,
} from '@/shared/ui/AttachmentPreview'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card, CardTitle } from '@/shared/ui/Card'
import { FilterChips } from '@/shared/ui/Chips'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { ConfirmDialog, Dialog } from '@/shared/ui/Dialog'
import { EmptyState } from '@/shared/ui/EmptyState'
import { FieldLabel, TextInput } from '@/shared/ui/Field'
import { PageHeader } from '@/shared/ui/PageHeader'
import { PageLoading } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'
import { EventTimeline } from './EventTimeline'

// 状态变化类事件到来时，刷新任务详情
const statusEvents = new Set([
  'task_started',
  'task_succeeded',
  'task_failed',
  'cancel_requested',
  'task_cancelled',
  'task_requeued',
])

const tabs = [
  { value: 'timeline', label: '执行过程' },
  { value: 'evidence', label: '证据' },
  { value: 'tools', label: '工具执行' },
]

function mergeBySeq(prev: TaskEvent[], ev: TaskEvent): TaskEvent[] {
  if (prev.some((p) => p.seq === ev.seq)) return prev
  return [...prev, ev].sort((a, b) => a.seq - b.seq)
}

function EvidenceTab({ taskId }: { taskId: string }) {
  const evidence = useQuery({
    queryKey: ['task-evidence', taskId],
    queryFn: () => api.getTaskEvidence(taskId),
    refetchInterval: 3000,
  })
  if (evidence.isPending) return <PageLoading />
  const items = evidence.data ?? []
  if (items.length === 0) {
    return (
      <EmptyState
        title="尚未采集到证据"
        description="证据在对应步骤执行后出现；取消或失败的任务保留已采集部分"
      />
    )
  }
  return (
    <div className="flex flex-col gap-3">
      {items.map((ev) => (
        <Card key={ev.evidenceId} className="bg-pearl p-5">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <div className="mb-1.5 flex items-center gap-2">
                <Badge tone={evidenceSourceMeta[ev.sourceType].tone}>
                  {evidenceSourceMeta[ev.sourceType].label}
                </Badge>
                <span className="text-[12px] text-ink-48">{ev.location}</span>
              </div>
              <p className="text-[13px] leading-[1.65] text-ink-80">{ev.summary}</p>
            </div>
            <time className="shrink-0 text-[12px] text-ink-48">
              {fmtDateTime(ev.collectedAt)}
            </time>
          </div>
        </Card>
      ))}
      <p className="text-[12px] text-ink-48">
        证据创建后不可原地修改；报告结论只能引用这里的证据。
      </p>
    </div>
  )
}

function ToolsTab({ taskId, isAdmin }: { taskId: string; isAdmin: boolean }) {
  const tools = useQuery({
    queryKey: ['task-tools', taskId],
    queryFn: () => api.getToolExecutions(taskId),
    refetchInterval: 3000,
  })

  const columns: Column<ToolExecution>[] = [
    { key: 'step', title: '步骤', render: (t) => t.stepName },
    {
      key: 'tool',
      title: '工具',
      render: (t) => <code className="text-[12px]">{t.toolName}</code>,
    },
    {
      key: 'status',
      title: '状态',
      render: (t) => (
        <Badge tone={toolStatusMeta[t.status].tone}>
          {toolStatusMeta[t.status].label}
        </Badge>
      ),
    },
    {
      key: 'duration',
      title: '耗时',
      render: (t) => <span className="tabular-nums">{t.durationMs} ms</span>,
    },
    {
      key: 'rows',
      title: '返回行数',
      render: (t) =>
        t.rowCount !== undefined ? (
          <span className="tabular-nums">
            {t.rowCount}
            {t.truncated && <span className="ml-1 text-[11px] text-warn">已截断</span>}
          </span>
        ) : (
          <span className="text-ink-48">—</span>
        ),
    },
    ...(isAdmin
      ? ([
          {
            key: 'tokens',
            title: 'Token / 成本',
            render: (t: ToolExecution) =>
              t.tokens !== undefined ? (
                <span className="tabular-nums">
                  {t.tokens}
                  {t.costText && (
                    <span className="ml-1.5 text-[12px] text-ink-48">{t.costText}</span>
                  )}
                </span>
              ) : (
                <span className="text-ink-48">—</span>
              ),
          },
        ] satisfies Column<ToolExecution>[])
      : []),
    {
      key: 'startedAt',
      title: '开始时间',
      className: 'text-ink-48',
      render: (t) => fmtDateTime(t.startedAt),
    },
  ]

  return (
    <div>
      <DataTable
        columns={columns}
        rows={tools.data ?? []}
        rowKey={(t) => t.executionId}
        loading={tools.isPending}
        emptyText="尚无工具执行记录"
      />
      <p className="mt-3 text-[12px] text-ink-48">
        {isAdmin
          ? 'Token 与成本仅 admin 可见；完整参数只保留在脱敏审计记录中。'
          : '工具参数已脱敏；Token 与成本仅 admin 可见。'}
      </p>
    </div>
  )
}

export function TaskDetailPage() {
  const { taskId = '' } = useParams()
  const navigate = useNavigate()
  const { user } = useAuth()
  const qc = useQueryClient()
  const toast = useToast()
  const [events, setEvents] = useState<TaskEvent[]>([])
  const [connState, setConnState] = useState<SseConnectionState>('connected')
  const [tab, setTab] = useState('timeline')
  const [cancelOpen, setCancelOpen] = useState(false)
  const [recoverOpen, setRecoverOpen] = useState(false)
  const [recoverReason, setRecoverReason] = useState(
    '依赖服务已恢复，重新执行未完成步骤',
  )

  const { previewName, openPreview, closePreview } = useAttachmentPreview()

  const task = useQuery({
    queryKey: ['task', taskId],
    queryFn: () => api.getTask(taskId),
  })
  // 用于展示“后续重试”反向关联
  const allTasks = useQuery({
    queryKey: ['tasks', 'all'],
    queryFn: () => api.listTasks({}),
  })

  // 模拟 SSE：补读历史 + 持续接收。真实实现换 EventSource(afterSeq / Last-Event-ID)。
  useEffect(() => {
    setEvents([])
    const unsubscribe = api.subscribeTaskEvents(
      taskId,
      0,
      (ev) => {
        setEvents((prev) => mergeBySeq(prev, ev))
        if (statusEvents.has(ev.type)) {
          qc.invalidateQueries({ queryKey: ['task', taskId] })
          qc.invalidateQueries({ queryKey: ['tasks'] })
        }
      },
      setConnState,
    )
    return unsubscribe
  }, [taskId, qc])

  const cancel = useMutation({
    mutationFn: () => api.cancelTask(taskId),
    onSuccess: () => {
      setCancelOpen(false)
      toast.success('已提交取消请求，将在步骤边界停止')
      qc.invalidateQueries({ queryKey: ['task', taskId] })
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : '取消失败'),
  })
  const recover = useMutation({
    mutationFn: (reason: string) => api.recoverTask(taskId, reason),
    onSuccess: () => {
      setRecoverOpen(false)
      toast.success('任务已重新入队（已记录审计）')
      qc.invalidateQueries({ queryKey: ['task', taskId] })
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : '恢复失败'),
  })

  if (task.isPending) return <PageLoading />
  if (task.isError || !task.data) {
    return <p className="py-24 text-center text-ink-48">任务不存在或无权访问</p>
  }
  const t = task.data
  const meta = taskStatusMeta[t.status]
  const running = t.status === 'pending' || t.status === 'running'
  const cancelling = t.status === 'cancel_requested'
  const terminal =
    t.status === 'succeeded' || t.status === 'failed' || t.status === 'cancelled'
  const retriedBy = (allTasks.data ?? []).filter(
    (x) => x.retryOfTaskId === t.taskId,
  )

  return (
    <div className="pb-24">
      <Link to="/tasks" className="press mb-4 inline-block text-[13px] text-primary">
        ‹ 返回任务列表
      </Link>
      <PageHeader
        eyebrow={
          <span>
            诊断任务 <code>{shortId(t.taskId)}</code>
          </span>
        }
        title={
          <span>
            {t.externalCaseKey}
            <span className="ml-3 text-ink-80">{t.caseTitle}</span>
          </span>
        }
        subtitle={
          <span className="flex items-center gap-2.5">
            <Badge tone={meta.tone} dot={running || cancelling}>
              {meta.label}
            </Badge>
            <span>
              {t.createdBy} 发起于 {fmtDateTime(t.createdAt)}
            </span>
          </span>
        }
        actions={
          <>
            {(running || cancelling) && (
              <Button
                variant="danger-ghost"
                disabled={cancelling || cancel.isPending}
                onClick={() => setCancelOpen(true)}
              >
                {cancelling ? '取消中…' : '取消任务'}
              </Button>
            )}
            {t.status === 'failed' && user?.role === 'admin' && !t.reportId && (
              <Button
                variant="ghost"
                disabled={recover.isPending}
                onClick={() => setRecoverOpen(true)}
              >
                恢复任务
              </Button>
            )}
            {terminal && (
              <Button
                variant="ghost"
                onClick={() =>
                  navigate(`/cases/${t.externalCaseId}/diagnose?retryOf=${t.taskId}`)
                }
              >
                重新诊断
              </Button>
            )}
            {t.reportId && (
              <Button onClick={() => navigate(`/tasks/${t.taskId}/report`)}>
                查看诊断报告
              </Button>
            )}
          </>
        }
      />

      {t.status === 'failed' && (
        <div className="mb-5 rounded-card border border-danger/25 bg-danger-soft px-5 py-4">
          <p className="text-[14px] font-semibold text-danger">诊断失败</p>
          <p className="mt-1 text-[13px] text-ink-80">
            {t.errorMessage ?? '发生不可恢复错误'}。任务事实已保留；
            {user?.role === 'admin'
              ? '可在满足恢复条件时重新入队（同一任务、同一消息 ID，记录审计）。'
              : '满足恢复条件时可由管理员重新入队。'}
          </p>
        </div>
      )}

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <div className="mb-4">
            <FilterChips options={tabs} value={tab} onChange={setTab} />
          </div>
          {tab === 'timeline' && (
            <Card className="p-6">
              <div className="mb-5 flex items-center justify-between gap-4">
                <CardTitle>执行过程</CardTitle>
                {running || cancelling ? (
                  connState === 'reconnecting' ? (
                    <Badge tone="orange" dot>
                      连接中断，正在重连…
                    </Badge>
                  ) : connState === 'polling' ? (
                    <Badge tone="orange" dot>
                      已降级为轮询
                    </Badge>
                  ) : (
                    <Badge tone="green" dot>
                      实时连接
                    </Badge>
                  )
                ) : (
                  <Badge tone="gray">已结束 · 事件已归档</Badge>
                )}
              </div>
              <EventTimeline events={events} live={running || cancelling} />
            </Card>
          )}
          {tab === 'evidence' && <EvidenceTab taskId={taskId} />}
          {tab === 'tools' && (
            <ToolsTab taskId={taskId} isAdmin={user?.role === 'admin'} />
          )}
        </div>

        <div className="flex flex-col gap-5">
          <Card className="p-6">
            <CardTitle className="mb-2">任务输入</CardTitle>
            <dl className="flex flex-col gap-3 text-[13px]">
              <div>
                <dt className="text-ink-48">证据数据源</dt>
                <dd className="mt-0.5 text-ink">{t.dataSourceNames.join('、')}</dd>
              </div>
              <div>
                <dt className="text-ink-48">补充说明</dt>
                <dd className="mt-0.5 text-ink">
                  {t.requestText || <span className="text-ink-48">未填写</span>}
                </dd>
              </div>
              <div>
                <dt className="text-ink-48">附件</dt>
                <dd className="mt-0.5 text-ink">
                  {t.attachmentNames.length > 0 ? (
                    <ul className="flex flex-col gap-1">
                      {t.attachmentNames.map((n) => (
                        <li key={n} className="text-[12px]">
                          <AttachmentName name={n} onOpen={openPreview} />
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <span className="text-ink-48">无</span>
                  )}
                </dd>
              </div>
              {(t.retryOfTaskId || retriedBy.length > 0) && (
                <div>
                  <dt className="text-ink-48">重试关联</dt>
                  <dd className="mt-0.5 flex flex-col gap-1 text-[12px]">
                    {t.retryOfTaskId && (
                      <span>
                        重试自
                        <Link
                          to={`/tasks/${t.retryOfTaskId}`}
                          className="press ml-1 text-primary hover:underline"
                        >
                          {shortId(t.retryOfTaskId)}
                        </Link>
                      </span>
                    )}
                    {retriedBy.map((r) => (
                      <span key={r.taskId}>
                        后续重试
                        <Link
                          to={`/tasks/${r.taskId}`}
                          className="press ml-1 text-primary hover:underline"
                        >
                          {shortId(r.taskId)}
                        </Link>
                      </span>
                    ))}
                  </dd>
                </div>
              )}
            </dl>
          </Card>

          <Card className="bg-pearl p-6">
            <p className="text-[12px] leading-[1.7] text-ink-48">
              任务基于发起时保存的不可变工单快照执行；外部工单后续变化不影响本次诊断。所有数据库访问只读、可审计。
            </p>
          </Card>
        </div>
      </div>

      <ConfirmDialog
        open={cancelOpen}
        title="取消诊断任务"
        message="取消后不再执行后续步骤，已完成的步骤、工具调用和证据会保留用于审计，且不生成正式报告。确认取消？"
        confirmLabel="确认取消"
        danger
        busy={cancel.isPending}
        onConfirm={() => cancel.mutate()}
        onCancel={() => setCancelOpen(false)}
      />

      <Dialog
        open={recoverOpen}
        title="恢复失败任务"
        onClose={() => setRecoverOpen(false)}
        footer={
          <>
            <Button variant="neutral" onClick={() => setRecoverOpen(false)}>
              取消
            </Button>
            <Button
              onClick={() => recover.mutate(recoverReason)}
              disabled={recover.isPending || !recoverReason.trim()}
            >
              {recover.isPending ? '提交中…' : '重新入队'}
            </Button>
          </>
        }
      >
        <p className="mb-4 text-[13px] leading-[1.7] text-ink-80">
          恢复将重用同一任务和消息 ID
          继续执行未完成步骤，不修改原请求范围、附件或数据源；操作会记录审计。
        </p>
        <FieldLabel>恢复原因</FieldLabel>
        <TextInput
          value={recoverReason}
          onChange={(e) => setRecoverReason(e.target.value)}
        />
      </Dialog>

      <AttachmentPreviewDialog name={previewName} onClose={closePreview} />
    </div>
  )
}
