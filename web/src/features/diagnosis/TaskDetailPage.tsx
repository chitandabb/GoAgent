import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RotateCcw } from 'lucide-react'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import { ApiError } from '@/shared/api'
import type { SseConnectionState, TaskEvent } from '@/shared/api/m1-types'
import { taskStatusMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card, CardTitle } from '@/shared/ui/Card'
import { FilterChips } from '@/shared/ui/Chips'
import { ConfirmDialog, Dialog } from '@/shared/ui/Dialog'
import { EmptyState } from '@/shared/ui/EmptyState'
import { FieldLabel, TextArea } from '@/shared/ui/Field'
import { PageHeader } from '@/shared/ui/PageHeader'
import { PageLoading } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'
import { EventTimeline } from './EventTimeline'
import { findWorkspaceForTask } from '@/features/workbench/workspace-store'

const tabs = [
  { value: 'timeline', label: '执行过程' },
  { value: 'evidence', label: '证据明细' },
  { value: 'tools', label: '工具执行' },
]

function mergeEvents(current: TaskEvent[], incoming: TaskEvent): TaskEvent[] {
  if (current.some((event) => event.seq === incoming.seq)) return current
  return [...current, incoming].sort((a, b) => a.seq - b.seq)
}

export function TaskDetailPage() {
  const { taskId = '' } = useParams()
  const navigate = useNavigate()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [events, setEvents] = useState<TaskEvent[]>([])
  const [connection, setConnection] = useState<SseConnectionState>('loading-history')
  const [streamError, setStreamError] = useState('')
  const [tab, setTab] = useState('timeline')
  const [cancelOpen, setCancelOpen] = useState(false)
  const [recoverOpen, setRecoverOpen] = useState(false)
  const [recoverReason, setRecoverReason] = useState('')
  const [streamGeneration, setStreamGeneration] = useState(0)
  const localWorkspace = findWorkspaceForTask(taskId)

  const task = useQuery({
    queryKey: ['task', taskId],
    queryFn: () => api.getTask(taskId),
    refetchInterval: (query) => {
      const value = query.state.data
      return value && ['pending', 'running', 'cancel_requested'].includes(value.status) ? 5000 : false
    },
  })
  const externalCase = useQuery({
    queryKey: ['external-case', task.data?.externalCaseId],
    queryFn: () => api.getExternalCase(task.data!.externalCaseId),
    enabled: !!task.data?.externalCaseId,
    retry: false,
  })

  useEffect(() => {
    setEvents([])
    setStreamError('')
    if (!task.isSuccess) return

    return api.subscribeTaskEvents(taskId, {
      onEvent: (event) => {
        setEvents((current) => mergeEvents(current, event))
        void queryClient.invalidateQueries({ queryKey: ['task', taskId] })
      },
      onStatus: (state) => {
        setConnection(state)
        if (state === 'connected') setStreamError('')
      },
      onTerminal: () => void queryClient.invalidateQueries({ queryKey: ['task', taskId] }),
      onError: (error) => {
        setStreamError(error.message)
      },
    })
  }, [queryClient, streamGeneration, task.isSuccess, taskId])

  const cancel = useMutation({
    mutationFn: () => api.cancelTask(taskId),
    onSuccess: (updated) => {
      queryClient.setQueryData(['task', taskId], updated)
      setCancelOpen(false)
      toast.success('已提交取消请求，将在任务边界停止')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '取消失败'),
  })
  const recover = useMutation({
    mutationFn: ({ reason, idempotencyKey }: { reason: string; idempotencyKey: string }) =>
      api.recoverTask(taskId, reason, idempotencyKey),
    retry: (failureCount, error) =>
      failureCount < 2 && error instanceof ApiError && error.code === 50301,
    onSuccess: () => {
      setRecoverOpen(false)
      setRecoverReason('')
      setStreamGeneration((value) => value + 1)
      toast.success('任务已重新入队，恢复操作已记录审计')
      void queryClient.invalidateQueries({ queryKey: ['task', taskId] })
    },
  })

  if (task.isPending) return <PageLoading />
  if (task.isError || !task.data) {
    return (
      <EmptyState
        title="任务不可读取"
        description={task.error instanceof Error ? task.error.message : '任务不存在或当前账号无权访问'}
        action={<Button variant="neutral" onClick={() => void task.refetch()}>重新加载</Button>}
      />
    )
  }

  const value = task.data
  const meta = taskStatusMeta[value.status]
  const active = ['pending', 'running', 'cancel_requested'].includes(value.status)
  const terminal = ['succeeded', 'failed', 'cancelled'].includes(value.status)
  const canRecover =
    user?.role === 'admin' &&
    value.status === 'failed' &&
    !value.reportAvailable &&
    value.lastErrorCode === 'agent_execution_failed'
  const caseTitle = externalCase.data
    ? `${externalCase.data.externalCaseKey} · ${externalCase.data.title}`
    : `工单 ${shortId(value.externalCaseId)}`

  return (
    <div className="pb-24">
      <Link to="/tasks" className="press mb-4 inline-block text-[13px] text-primary">‹ 返回最近任务</Link>
      <PageHeader
        eyebrow={<span>诊断任务 <code>{shortId(value.taskId)}</code></span>}
        title={caseTitle}
        subtitle={
          <span className="flex flex-wrap items-center gap-2.5">
            <Badge tone={meta.tone} dot={active}>{meta.label}</Badge>
            <span>创建于 {fmtDateTime(value.createdAt)} · 第 {value.attemptCount} 次执行</span>
          </span>
        }
        actions={
          <>
            {localWorkspace && (
              <Button variant="neutral" onClick={() => navigate(`/workbench/${localWorkspace.workspaceId}`)}>
                在工作台打开
              </Button>
            )}
            {active && (
              <Button
                variant="danger-ghost"
                disabled={value.status === 'cancel_requested' || cancel.isPending}
                onClick={() => setCancelOpen(true)}
              >
                {value.status === 'cancel_requested' ? '取消中…' : '取消任务'}
              </Button>
            )}
            {canRecover && <Button variant="ghost" onClick={() => setRecoverOpen(true)}>恢复任务</Button>}
            {terminal && (
              <Button variant="ghost" onClick={() => navigate(`/cases/${value.externalCaseId}/diagnose?retryOf=${value.taskId}`)}>
                重新诊断
              </Button>
            )}
            {value.reportAvailable && (
              <Button onClick={() => navigate(`/tasks/${value.taskId}/report`)}>查看正式报告</Button>
            )}
          </>
        }
      />

      {value.status === 'failed' && (
        <div className="mb-5 rounded-utility border border-danger/25 bg-danger-soft px-5 py-4">
          <p className="text-[14px] font-semibold text-danger">{value.lastErrorCode || '诊断失败'}</p>
          <p className="mt-1 text-[13px] leading-[1.6] text-ink-80">{value.lastErrorMessage || '后端未提供更多失败信息'}</p>
          {user?.role === 'admin' && !canRecover && (
            <p className="mt-2 text-[12px] text-ink-48">该错误不满足后端允许的管理员恢复条件。</p>
          )}
        </div>
      )}

      <div className="grid gap-5 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <div className="mb-4"><FilterChips options={tabs} value={tab} onChange={setTab} /></div>
          {tab === 'timeline' ? (
            <Card className="p-6">
              <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
                <CardTitle>执行过程</CardTitle>
                {connection === 'loading-history' ? (
                  <Badge tone="gray" dot>正在补读历史</Badge>
                ) : connection === 'reconnecting' ? (
                  <Badge tone="orange" dot>连接中断，正在续传</Badge>
                ) : connection === 'connected' ? (
                  <Badge tone="green" dot>实时连接</Badge>
                ) : connection === 'failed' ? (
                  <Badge tone="red">事件连接已停止</Badge>
                ) : (
                  <Badge tone="gray">事件流已结束</Badge>
                )}
              </div>
              {streamError && (
                <div className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-utility bg-warn-soft px-4 py-3 text-[12px] text-warn">
                  <p>{streamError}。已保留收到的事件。</p>
                  {connection === 'failed' && (
                    <Button
                      size="sm"
                      variant="neutral"
                      onClick={() => {
                        setStreamError('')
                        setConnection('loading-history')
                        setStreamGeneration((value) => value + 1)
                      }}
                    >
                      <RotateCcw />
                      重新连接
                    </Button>
                  )}
                </div>
              )}
              <EventTimeline events={events} live={active && connection !== 'closed' && connection !== 'failed'} />
            </Card>
          ) : (
            <Card className="p-6">
              <EmptyState
                title={tab === 'evidence' ? '证据明细接口尚未开放' : '工具执行接口尚未开放'}
                description={tab === 'evidence'
                  ? '当前只能在正式报告中读取有序证据声明元数据，不能读取或伪造原始证据内容。'
                  : '当前后端未提供独立工具执行列表，页面不会继续显示 Mock 记录。'}
              />
            </Card>
          )}
        </div>

        <div className="flex flex-col gap-5">
          <Card className="p-6">
            <CardTitle className="mb-3">任务输入</CardTitle>
            <dl className="flex flex-col gap-3 text-[13px]">
              <div><dt className="text-ink-48">补充说明</dt><dd className="mt-0.5 whitespace-pre-wrap break-words">{value.requestText}</dd></div>
              <div><dt className="text-ink-48">快照 ID</dt><dd className="mt-0.5"><code className="text-[12px] text-ink-48">{shortId(value.caseSnapshotId)}</code></dd></div>
              {value.retryOfTaskId && <div><dt className="text-ink-48">重试自</dt><dd className="mt-0.5"><Link className="text-primary" to={`/tasks/${value.retryOfTaskId}`}>{shortId(value.retryOfTaskId)}</Link></dd></div>}
            </dl>
          </Card>
          <Card className="bg-pearl p-6">
            <p className="text-[12px] leading-[1.7] text-ink-48">任务基于创建时保存的不可变工单快照执行。关闭浏览器或 SSE 断开都不会取消任务。</p>
          </Card>
        </div>
      </div>

      <ConfirmDialog
        open={cancelOpen}
        title="取消诊断任务"
        message="取消请求只会在后端任务边界生效；浏览器断开不会触发取消。确认提交？"
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
            <Button variant="neutral" onClick={() => setRecoverOpen(false)}>取消</Button>
            <Button
              disabled={recover.isPending || !recoverReason.trim()}
              onClick={() => recover.mutate({ reason: recoverReason, idempotencyKey: api.createIdempotencyKey() })}
            >
              {recover.isPending ? '提交中…' : '重新入队'}
            </Button>
          </>
        }
      >
        <p className="mb-4 text-[13px] leading-[1.7] text-ink-80">恢复会保留原任务输入与创建时冻结的授权策略，并由后端判断当前错误、依赖和状态是否仍允许恢复。</p>
        <FieldLabel htmlFor="recover-reason">恢复原因</FieldLabel>
        <TextArea id="recover-reason" value={recoverReason} maxLength={1000} onChange={(event) => setRecoverReason(event.target.value)} />
        {recover.isError && <p className="mt-3 text-[13px] text-danger">{recover.error instanceof Error ? recover.error.message : '恢复失败'}</p>}
      </Dialog>
    </div>
  )
}
