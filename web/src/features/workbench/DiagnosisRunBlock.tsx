import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ExternalLink, RotateCcw } from 'lucide-react'
import { Link } from 'react-router'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import { ApiError } from '@/shared/api'
import type { SseConnectionState, TaskEvent } from '@/shared/api/m1-types'
import { taskStatusMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { ConfirmDialog, Dialog } from '@/shared/ui/Dialog'
import { EmptyState } from '@/shared/ui/EmptyState'
import { FieldLabel, TextArea } from '@/shared/ui/Field'
import { useToast } from '@/shared/ui/Toast'
import { EventTimeline } from '@/features/diagnosis/EventTimeline'
import { InlineReport } from './InlineReport'

function mergeEvents(current: TaskEvent[], incoming: TaskEvent): TaskEvent[] {
  if (current.some((event) => event.seq === incoming.seq)) return current
  return [...current, incoming].sort((a, b) => a.seq - b.seq)
}

export function DiagnosisRunBlock({ taskId }: { taskId: string }) {
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [events, setEvents] = useState<TaskEvent[]>([])
  const [connection, setConnection] = useState<SseConnectionState>('loading-history')
  const [streamError, setStreamError] = useState('')
  const [cancelOpen, setCancelOpen] = useState(false)
  const [recoverOpen, setRecoverOpen] = useState(false)
  const [recoverReason, setRecoverReason] = useState('')
  const [streamGeneration, setStreamGeneration] = useState(0)

  const task = useQuery({
    queryKey: ['task', taskId],
    queryFn: () => api.getTask(taskId),
    refetchInterval: (query) => {
      const value = query.state.data
      return value && ['pending', 'running', 'cancel_requested'].includes(value.status) ? 5000 : false
    },
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
      toast.success('已提交取消请求')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '取消失败'),
  })
  const recover = useMutation({
    mutationFn: ({ reason, idempotencyKey }: { reason: string; idempotencyKey: string }) =>
      api.recoverTask(taskId, reason, idempotencyKey),
    retry: (failureCount, error) => failureCount < 2 && error instanceof ApiError && error.code === 50301,
    onSuccess: () => {
      setRecoverOpen(false)
      setRecoverReason('')
      setStreamGeneration((value) => value + 1)
      toast.success('任务已重新入队')
      void queryClient.invalidateQueries({ queryKey: ['task', taskId] })
    },
  })

  if (task.isPending) {
    return <Card className="h-36 animate-pulse bg-canvas" />
  }
  if (task.isError || !task.data) {
    return (
      <Card className="p-5">
        <EmptyState
          title="任务不可读取"
          description={task.error instanceof Error ? task.error.message : '任务不存在或当前账号无权访问'}
          action={<Button size="sm" variant="neutral" onClick={() => void task.refetch()}>重新加载</Button>}
        />
      </Card>
    )
  }

  const value = task.data
  const active = ['pending', 'running', 'cancel_requested'].includes(value.status)
  const canRecover = user?.role === 'admin' && value.status === 'failed' && !value.reportAvailable && value.lastErrorCode === 'agent_execution_failed'

  return (
    <article>
      <div className="mb-3 flex justify-end">
        <div className="max-w-[84%] rounded-[18px] rounded-br-[6px] bg-primary px-4 py-3 text-white sm:max-w-[72%]">
          <p className="whitespace-pre-wrap text-[13px] leading-[1.65]">{value.requestText}</p>
          <p className="mt-1.5 text-[10px] text-white/65">{fmtDateTime(value.createdAt)}</p>
        </div>
      </div>

      <div className="flex items-start gap-3">
        <div className="mt-1 flex size-7 shrink-0 items-center justify-center rounded-utility bg-ink text-[10px] font-semibold text-white">MG</div>
        <Card className="min-w-0 flex-1 overflow-hidden">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-divider px-5 py-3.5">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <Badge tone={taskStatusMeta[value.status].tone} dot={active}>{taskStatusMeta[value.status].label}</Badge>
              <span className="text-[11px] text-ink-48">运行 {shortId(value.taskId)} · 第 {value.attemptCount} 次执行</span>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {active && (
                <Button size="sm" variant="danger-ghost" disabled={value.status === 'cancel_requested' || cancel.isPending} onClick={() => setCancelOpen(true)}>
                  {value.status === 'cancel_requested' ? '取消中…' : '取消'}
                </Button>
              )}
              {canRecover && <Button size="sm" variant="ghost" onClick={() => setRecoverOpen(true)}>恢复</Button>}
              <Button asChild size="icon" variant="neutral" className="!size-8" title="打开任务深链接">
                <Link to={`/tasks/${value.taskId}`}><ExternalLink /><span className="sr-only">打开任务深链接</span></Link>
              </Button>
            </div>
          </div>

          {value.status === 'failed' && (
            <div className="border-b border-divider bg-danger-soft px-5 py-3">
              <p className="text-[12px] font-semibold text-danger">{value.lastErrorCode || '诊断失败'}</p>
              <p className="mt-1 text-[11px] leading-[1.55] text-ink-80">{value.lastErrorMessage || '后端未提供更多失败信息'}</p>
            </div>
          )}

          <div className="px-5 py-5">
            <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
              <h3 className="text-[12px] font-semibold text-ink">执行过程</h3>
              {connection === 'loading-history' ? <Badge tone="gray" dot>补读历史</Badge> : connection === 'reconnecting' ? <Badge tone="orange" dot>连接中断，正在续传</Badge> : connection === 'connected' ? <Badge tone="green" dot>实时连接</Badge> : connection === 'failed' ? <Badge tone="red">事件连接已停止</Badge> : <Badge tone="gray">事件流已结束</Badge>}
            </div>
            {streamError && (
              <div className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-utility bg-warn-soft px-3 py-2 text-[11px] text-warn">
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
          </div>

          {value.reportAvailable && <InlineReport taskId={taskId} />}
        </Card>
      </div>

      <ConfirmDialog
        open={cancelOpen}
        title="取消诊断任务"
        message="取消请求只会在后端任务边界生效；关闭工作台或断开事件流不会触发取消。"
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
            <Button disabled={recover.isPending || !recoverReason.trim()} onClick={() => recover.mutate({ reason: recoverReason, idempotencyKey: api.createIdempotencyKey() })}>{recover.isPending ? '提交中…' : '重新入队'}</Button>
          </>
        }
      >
        <p className="mb-4 text-[13px] leading-[1.65] text-ink-80">恢复保留原任务、工单快照和调查范围，后端决定当前状态是否允许恢复。</p>
        <FieldLabel htmlFor={`recover-${taskId}`}>恢复原因</FieldLabel>
        <TextArea id={`recover-${taskId}`} value={recoverReason} maxLength={1000} onChange={(event) => setRecoverReason(event.target.value)} />
        {recover.isError && <p className="mt-3 text-[12px] text-danger">{recover.error instanceof Error ? recover.error.message : '恢复失败'}</p>}
      </Dialog>
    </article>
  )
}
