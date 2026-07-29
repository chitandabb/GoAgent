import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type { DiagnosisTask } from '@/shared/api'
import { taskStatusMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { FilterChips } from '@/shared/ui/Chips'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { Select } from '@/shared/ui/Field'
import { PageHeader } from '@/shared/ui/PageHeader'

const statusOptions = [
  { value: 'all', label: '全部' },
  { value: 'running', label: '执行中' },
  { value: 'succeeded', label: '已完成' },
  { value: 'failed', label: '失败' },
  { value: 'cancelled', label: '已取消' },
]

export function TasksPage() {
  const navigate = useNavigate()
  const { user } = useAuth()
  const [status, setStatus] = useState('all')
  const [createdBy, setCreatedBy] = useState('all')
  const isAdmin = user?.role === 'admin'

  const tasks = useQuery({
    queryKey: ['tasks', status, createdBy],
    queryFn: () => api.listTasks({ status, createdBy }),
    // 列表页轮询兜底（详情页才用 SSE）
    refetchInterval: 5000,
  })
  // admin 可按发起人筛选全部任务（api.md createdBy）
  const users = useQuery({
    queryKey: ['admin-users'],
    queryFn: api.listUsers,
    enabled: isAdmin,
  })

  const columns: Column<DiagnosisTask>[] = [
    {
      key: 'id',
      title: '任务',
      render: (t) => (
        <code className="text-[12px] text-ink-48">{shortId(t.taskId)}</code>
      ),
    },
    {
      key: 'case',
      title: '工单',
      className: 'max-w-[340px]',
      render: (t) => (
        <span>
          <span className="font-semibold">{t.externalCaseKey}</span>
          <span className="ml-2 text-ink-80">{t.caseTitle}</span>
        </span>
      ),
    },
    {
      key: 'status',
      title: '状态',
      render: (t) => (
        <Badge tone={taskStatusMeta[t.status].tone} dot={t.status === 'running'}>
          {taskStatusMeta[t.status].label}
        </Badge>
      ),
    },
    { key: 'creator', title: '发起人', render: (t) => t.createdBy },
    {
      key: 'createdAt',
      title: '创建时间',
      className: 'text-ink-48',
      render: (t) => fmtDateTime(t.createdAt),
    },
    {
      key: 'completedAt',
      title: '完成时间',
      className: 'text-ink-48',
      render: (t) => fmtDateTime(t.completedAt),
    },
    {
      key: 'report',
      title: '报告',
      render: (t) =>
        t.reportId ? (
          <span className="text-[13px] text-primary">查看报告</span>
        ) : (
          <span className="text-[13px] text-ink-48">—</span>
        ),
    },
  ]

  return (
    <div>
      <PageHeader
        title="诊断任务"
        subtitle="任务在后台持久执行；关闭页面不会中断，重新进入可恢复进度"
        actions={
          <div className="flex items-center gap-3">
            {isAdmin && (
              <Select
                value={createdBy}
                onValueChange={setCreatedBy}
                className="!w-40"
              >
                <option value="all">全部发起人</option>
                {(users.data ?? []).map((u) => (
                  <option key={u.id} value={u.username}>
                    {u.displayName}
                  </option>
                ))}
              </Select>
            )}
            <FilterChips options={statusOptions} value={status} onChange={setStatus} />
          </div>
        }
      />
      <DataTable
        columns={columns}
        rows={tasks.data ?? []}
        rowKey={(t) => t.taskId}
        onRowClick={(t) => navigate(`/tasks/${t.taskId}`)}
        loading={tasks.isPending}
        emptyText="还没有诊断任务，先从外部工单发起一次诊断"
      />
    </div>
  )
}
