import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import type { DiagnosisTaskListItem, TaskStatus } from '@/shared/api/m1-types'
import * as api from '@/shared/api'
import { taskStatusMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { FilterChips } from '@/shared/ui/Chips'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { EmptyState } from '@/shared/ui/EmptyState'
import { TextInput } from '@/shared/ui/Field'
import { PageHeader } from '@/shared/ui/PageHeader'

const statusOptions = [
  { value: 'all', label: '全部' },
  { value: 'running', label: '进行中' },
  { value: 'succeeded', label: '已完成' },
  { value: 'failed', label: '失败' },
  { value: 'cancelled', label: '已取消' },
]

const runningStatuses: TaskStatus[] = ['pending', 'running', 'cancel_requested']

export function TasksPage() {
  const navigate = useNavigate()
  const [status, setStatus] = useState('all')
  const [page, setPage] = useState(1)
  const [taskId, setTaskId] = useState('')

  const query = useQuery({
    queryKey: ['diagnosis-tasks', status, page],
    queryFn: () =>
      api.listDiagnosisTasks({
        status: status === 'all' ? undefined : (status as TaskStatus),
        page,
        pageSize: 20,
      }),
    // 列表里还有活跃任务时轮询刷新状态
    refetchInterval: (q) =>
      (q.state.data?.items ?? []).some((item) => runningStatuses.includes(item.status)) ? 5000 : false,
  })

  const tasks = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / 20))

  const columns: Column<DiagnosisTaskListItem>[] = [
    {
      key: 'case',
      title: '工单',
      className: 'max-w-[300px]',
      render: (task) => (
        <div>
          <p className="font-semibold text-ink">{task.externalCaseKey || shortId(task.externalCaseId)}</p>
          {task.externalCaseTitle && (
            <p className="line-clamp-1 text-[12px] text-ink-48">{task.externalCaseTitle}</p>
          )}
        </div>
      ),
    },
    {
      key: 'taskId',
      title: '任务',
      render: (task) => <code className="text-[12px] text-ink-48">{shortId(task.taskId)}</code>,
    },
    {
      key: 'request',
      title: '诊断请求',
      className: 'max-w-[260px]',
      render: (task) => (
        <p className="line-clamp-1 text-[12px] text-ink-80">{task.requestText}</p>
      ),
    },
    {
      key: 'status',
      title: '状态',
      render: (task) => (
        <Badge tone={taskStatusMeta[task.status].tone} dot={runningStatuses.includes(task.status)}>
          {taskStatusMeta[task.status].label}
        </Badge>
      ),
    },
    {
      key: 'attempt',
      title: '执行次数',
      render: (task) => <span className="tabular-nums">{task.attemptCount}</span>,
    },
    {
      key: 'createdAt',
      title: '创建时间',
      className: 'text-ink-48',
      render: (task) => fmtDateTime(task.createdAt),
    },
    {
      key: 'report',
      title: '报告',
      render: (task) =>
        task.reportAvailable ? <span className="text-primary">可查看</span> : <span className="text-ink-48">—</span>,
    },
  ]

  const openTask = () => {
    const value = taskId.trim()
    if (value) navigate(`/tasks/${value}`)
  }

  return (
    <div>
      <PageHeader
        title="诊断任务"
        subtitle="从服务端读取你创建过的全部诊断任务；管理员可查看所有任务"
        actions={<FilterChips options={statusOptions} value={status} onChange={(value) => { setStatus(value); setPage(1) }} />}
      />

      <div className="mb-5 flex flex-wrap items-center gap-2 rounded-utility border border-hairline bg-pearl px-4 py-3">
        <TextInput
          value={taskId}
          onChange={(event) => setTaskId(event.target.value)}
          onKeyDown={(event) => event.key === 'Enter' && openTask()}
          placeholder="输入已知 taskId 直接打开"
          className="max-w-md bg-canvas"
          aria-label="任务 ID"
        />
        <Button variant="neutral" onClick={openTask} disabled={!taskId.trim()}>
          打开任务
        </Button>
      </div>

      {query.isPending && tasks.length === 0 ? (
        <DataTable columns={columns} rows={[]} rowKey={(task) => task.taskId} loading />
      ) : tasks.length === 0 ? (
        <EmptyState
          title={status === 'all' ? '还没有诊断任务' : '当前筛选下没有任务'}
          description="从工单详情页发起诊断后，任务会出现在这里。"
          action={<Button onClick={() => navigate('/cases')}>前往外部工单</Button>}
        />
      ) : (
        <>
          <DataTable
            columns={columns}
            rows={tasks}
            rowKey={(task) => task.taskId}
            onRowClick={(task) => navigate(`/tasks/${task.taskId}`)}
            loading={query.isFetching}
            emptyText="当前筛选下没有任务"
          />
          {total > 20 && (
            <div className="mt-4 flex items-center justify-between text-[12px] text-ink-48">
              <span>共 {total} 条 · 第 {page} / {totalPages} 页</span>
              <div className="flex items-center gap-2">
                <Button variant="neutral" size="sm" disabled={page <= 1 || query.isFetching} onClick={() => setPage((value) => value - 1)}>
                  上一页
                </Button>
                <Button variant="neutral" size="sm" disabled={page >= totalPages || query.isFetching} onClick={() => setPage((value) => value + 1)}>
                  下一页
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
