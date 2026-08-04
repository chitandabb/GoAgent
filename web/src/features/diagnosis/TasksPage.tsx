import { useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { useQueries } from '@tanstack/react-query'
import type { DiagnosisTask } from '@/shared/api/m1-types'
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

export function TasksPage() {
  const navigate = useNavigate()
  const [status, setStatus] = useState('all')
  const [taskId, setTaskId] = useState('')
  const entries = useMemo(() => api.getRecentTasks(), [])
  const queries = useQueries({
    queries: entries.map((entry) => ({
      queryKey: ['task', entry.taskId],
      queryFn: () => api.getTask(entry.taskId),
      refetchInterval: (query: { state: { data?: DiagnosisTask } }) => {
        const current = query.state.data
        return current && ['pending', 'running', 'cancel_requested'].includes(current.status)
          ? 5000
          : false
      },
      retry: false,
    })),
  })

  const tasks = queries
    .flatMap((query) => (query.data ? [query.data] : []))
    .filter((task) => {
      if (status === 'all') return true
      if (status === 'running') {
        return ['pending', 'running', 'cancel_requested'].includes(task.status)
      }
      return task.status === status
    })
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))

  const columns: Column<DiagnosisTask>[] = [
    {
      key: 'id',
      title: '任务',
      render: (task) => <code className="text-[12px] text-ink-48">{shortId(task.taskId)}</code>,
    },
    {
      key: 'scope',
      title: '调查范围',
      render: (task) => (
        <span className="text-[13px]">
          {(task.requestScope.allowedCapabilities ?? ['case']).join(' / ')}
        </span>
      ),
    },
    {
      key: 'status',
      title: '状态',
      render: (task) => (
        <Badge tone={taskStatusMeta[task.status].tone} dot={['pending', 'running', 'cancel_requested'].includes(task.status)}>
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
      render: (task) => task.reportAvailable ? <span className="text-primary">可查看</span> : <span className="text-ink-48">—</span>,
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
        subtitle="当前后端没有任务列表接口；这里仅保留本浏览器会话创建过的任务入口，状态始终从服务端读取"
        actions={<FilterChips options={statusOptions} value={status} onChange={setStatus} />}
      />

      <div className="mb-5 flex flex-wrap items-center gap-2 rounded-utility border border-hairline bg-pearl px-4 py-3">
        <TextInput
          value={taskId}
          onChange={(event) => setTaskId(event.target.value)}
          onKeyDown={(event) => event.key === 'Enter' && openTask()}
          placeholder="输入已知 taskId"
          className="max-w-md bg-canvas"
          aria-label="任务 ID"
        />
        <Button variant="neutral" onClick={openTask} disabled={!taskId.trim()}>
          打开任务
        </Button>
      </div>

      {entries.length === 0 ? (
        <EmptyState
          title="本会话还没有最近任务"
          description="请从真实 ERP 工单详情发起诊断，或在上方输入已知 taskId。这里不是服务端任务列表。"
          action={<Button onClick={() => navigate('/cases')}>前往外部工单</Button>}
        />
      ) : (
        <>
          <DataTable
            columns={columns}
            rows={tasks}
            rowKey={(task) => task.taskId}
            onRowClick={(task) => navigate(`/tasks/${task.taskId}`)}
            loading={queries.some((query) => query.isPending)}
            emptyText="当前筛选下没有可读取的最近任务"
          />
          {queries.some((query) => query.isError) && (
            <p className="mt-3 text-[12px] text-warn">
              部分导航记录已失效或当前账号无权读取；页面没有用本地状态替代后端结果。
            </p>
          )}
        </>
      )}
    </div>
  )
}
