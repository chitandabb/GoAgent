import { useNavigate } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as api from '@/shared/api'
import type { DeadLetterMessage, DiagnosisTask } from '@/shared/api'
import { depStatusMeta, taskStatusMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { PageLoading } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'

function Stat({ label, value, alarm }: { label: string; value: number; alarm?: boolean }) {
  return (
    <Card className="p-5">
      <p className="text-[12px] text-ink-48">{label}</p>
      <p
        className={`mt-1 text-[28px] font-semibold tabular-nums leading-none ${
          alarm && value > 0 ? 'text-danger' : 'text-ink'
        }`}
      >
        {value}
      </p>
    </Card>
  )
}

function SectionTitle({ title, note }: { title: string; note?: string }) {
  return (
    <div className="mb-3 mt-8 flex items-baseline gap-3">
      <h2 className="text-[16px] font-semibold text-ink">{title}</h2>
      {note && <span className="text-[12px] text-ink-48">{note}</span>}
    </div>
  )
}

export function SystemPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()

  const deps = useQuery({
    queryKey: ['admin-deps'],
    queryFn: api.getDependencies,
    refetchInterval: 10_000,
  })
  const stats = useQuery({
    queryKey: ['admin-stats'],
    queryFn: api.getSystemStats,
    refetchInterval: 10_000,
  })
  const failedTasks = useQuery({
    queryKey: ['tasks', 'failed', 'all'],
    queryFn: () => api.listTasks({ status: 'failed' }),
    refetchInterval: 10_000,
  })
  const deadLetters = useQuery({
    queryKey: ['dead-letters'],
    queryFn: api.listDeadLetters,
  })

  const requeue = useMutation({
    mutationFn: (messageId: string) => api.requeueDeadLetter(messageId),
    onSuccess: () => {
      toast.success('已重新投递（演示）：消息回到工作队列，由 Worker 幂等消费')
      qc.invalidateQueries({ queryKey: ['dead-letters'] })
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : '重新投递失败'),
  })

  if (deps.isPending || stats.isPending) return <PageLoading />
  const s = stats.data!

  const failedColumns: Column<DiagnosisTask>[] = [
    {
      key: 'id',
      title: '任务',
      render: (t) => <code className="text-[12px] text-ink-48">{shortId(t.taskId)}</code>,
    },
    {
      key: 'case',
      title: '工单',
      render: (t) => (
        <span>
          <span className="font-semibold">{t.externalCaseKey}</span>
          <span className="ml-2 text-ink-80">{t.caseTitle}</span>
        </span>
      ),
    },
    {
      key: 'error',
      title: '失败原因',
      className: 'max-w-[260px]',
      render: (t) => (
        <span className="line-clamp-1 text-danger">{t.errorMessage ?? '—'}</span>
      ),
    },
    { key: 'creator', title: '发起人', render: (t) => t.createdBy },
    {
      key: 'completedAt',
      title: '失败时间',
      className: 'text-ink-48',
      render: (t) => fmtDateTime(t.completedAt),
    },
    {
      key: 'status',
      title: '状态',
      render: (t) => (
        <Badge tone={taskStatusMeta[t.status].tone}>{taskStatusMeta[t.status].label}</Badge>
      ),
    },
  ]

  const dlColumns: Column<DeadLetterMessage>[] = [
    {
      key: 'messageId',
      title: '消息',
      render: (d) => <code className="text-[12px]">{d.messageId}</code>,
    },
    { key: 'queue', title: '队列', render: (d) => <code className="text-[12px] text-ink-48">{d.queue}</code> },
    {
      key: 'reason',
      title: '死信原因',
      className: 'max-w-[280px]',
      render: (d) => <span className="line-clamp-1">{d.reason}</span>,
    },
    {
      key: 'attempts',
      title: '投递次数',
      render: (d) => <span className="tabular-nums">{d.attempts}</span>,
    },
    {
      key: 'occurredAt',
      title: '进入时间',
      className: 'text-ink-48',
      render: (d) => fmtDateTime(d.occurredAt),
    },
    {
      key: 'actions',
      title: '操作',
      render: (d) => (
        <Button
          variant="neutral"
          size="sm"
          onClick={() => requeue.mutate(d.messageId)}
          disabled={requeue.isPending}
        >
          重新投递
        </Button>
      ),
    },
  ]

  return (
    <div>
      <div className="mb-6 grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Stat label="执行中 / 等待任务" value={s.runningTasks} />
        <Stat label="队列积压" value={s.queueBacklog} alarm />
        <Stat label="Outbox 未发布" value={s.outboxUnpublished} alarm />
        <Stat label="失败任务" value={s.failedTasks24h} alarm />
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {(deps.data ?? []).map((d) => {
          const meta = depStatusMeta[d.status]
          return (
            <Card key={d.name} className="p-5">
              <div className="mb-1 flex items-center justify-between gap-3">
                <p className="text-[14px] font-semibold text-ink">{d.name}</p>
                <Badge tone={meta.tone} dot>
                  {meta.label}
                </Badge>
              </div>
              <p className="text-[12px] text-ink-48">{d.kind}</p>
              <div className="mt-3 flex items-center justify-between text-[12px] text-ink-48">
                <span>延迟 {d.latencyMs} ms</span>
                <span>{fmtDateTime(d.checkedAt)}</span>
              </div>
              {d.message && (
                <p className="mt-2 rounded-capsule bg-warn-soft px-3 py-2 text-[12px] leading-[1.5] text-warn">
                  {d.message}
                </p>
              )}
            </Card>
          )
        })}
      </div>

      <SectionTitle
        title="失败任务"
        note="点击进入任务详情；满足恢复条件时可在详情页重新入队（记录审计）"
      />
      <DataTable
        columns={failedColumns}
        rows={failedTasks.data ?? []}
        rowKey={(t) => t.taskId}
        onRowClick={(t) => navigate(`/tasks/${t.taskId}`)}
        loading={failedTasks.isPending}
        emptyText="当前没有失败任务"
      />

      <SectionTitle
        title="死信队列"
        note="重复投递超限或不可处理的消息；重新投递前应先排除原因"
      />
      <DataTable
        columns={dlColumns}
        rows={deadLetters.data ?? []}
        rowKey={(d) => d.messageId}
        loading={deadLetters.isPending}
        emptyText="死信队列为空"
      />
    </div>
  )
}
