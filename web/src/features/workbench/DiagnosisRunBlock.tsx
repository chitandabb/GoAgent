import { ExternalLink, RotateCcw } from 'lucide-react'
import { Link } from 'react-router'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import { taskStatusMeta } from '@/shared/lib/status'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { ConfirmDialog, Dialog } from '@/shared/ui/Dialog'
import { EmptyState } from '@/shared/ui/EmptyState'
import { FieldLabel, TextArea } from '@/shared/ui/Field'
import { EventTimeline } from '@/shared/diagnosis-run/EventTimeline'
import { useDiagnosisRun } from '@/shared/diagnosis-run/useDiagnosisRun'
import { InlineReport } from './InlineReport'

export function DiagnosisRunBlock({ taskId }: { taskId: string }) {
  const { user } = useAuth()
  const {
    task,
    events,
    connection,
    streamError,
    reconnect,
    cancel,
    cancelOpen,
    setCancelOpen,
    recover,
    recoverOpen,
    setRecoverOpen,
    recoverReason,
    setRecoverReason,
  } = useDiagnosisRun(taskId)

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
              <span className="text-[11px] text-ink-48">第 {value.attemptCount} 次处理</span>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {active && (
                <Button size="sm" variant="danger-ghost" disabled={value.status === 'cancel_requested' || cancel.isPending} onClick={() => setCancelOpen(true)}>
                  {value.status === 'cancel_requested' ? '取消中…' : '取消'}
                </Button>
              )}
              {canRecover && <Button size="sm" variant="ghost" onClick={() => setRecoverOpen(true)}>恢复</Button>}
              <Button asChild size="icon" variant="neutral" className="!size-8" title="查看任务详情">
                <Link to={`/tasks/${value.taskId}`}><ExternalLink /><span className="sr-only">查看任务详情</span></Link>
              </Button>
            </div>
          </div>

          {value.status === 'failed' && (
            <div className="border-b border-divider bg-danger-soft px-5 py-3">
              <p className="text-[12px] font-semibold text-danger">{value.lastErrorCode || '处理失败'}</p>
              <p className="mt-1 text-[11px] leading-[1.55] text-ink-80">{value.lastErrorMessage || '暂时没有更多失败信息'}</p>
            </div>
          )}

          <div className="px-5 py-5">
            <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
              <h3 className="text-[12px] font-semibold text-ink">处理进度</h3>
              {connection === 'loading-history' ? <Badge tone="gray" dot>正在读取进度</Badge> : connection === 'reconnecting' ? <Badge tone="orange" dot>连接中断，正在恢复</Badge> : connection === 'connected' ? <Badge tone="green" dot>进度已连接</Badge> : connection === 'failed' ? <Badge tone="red">进度连接已停止</Badge> : <Badge tone="gray">处理已结束</Badge>}
            </div>
            {streamError && (
              <div className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-utility bg-warn-soft px-3 py-2 text-[11px] text-warn">
                <p>{streamError}。已保留收到的事件。</p>
                {connection === 'failed' && (
                  <Button
                    size="sm"
                    variant="neutral"
                    onClick={reconnect}
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
        title="取消排查任务"
        message="确认取消当前排查任务？已经完成的处理记录会保留。"
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
        <p className="mb-4 text-[13px] leading-[1.65] text-ink-80">恢复后会继续使用原工单信息和排查范围。</p>
        <FieldLabel htmlFor={`recover-${taskId}`}>恢复原因</FieldLabel>
        <TextArea id={`recover-${taskId}`} value={recoverReason} maxLength={1000} onChange={(event) => setRecoverReason(event.target.value)} />
        {recover.isError && <p className="mt-3 text-[12px] text-danger">{recover.error instanceof Error ? recover.error.message : '恢复失败'}</p>}
      </Dialog>
    </article>
  )
}
