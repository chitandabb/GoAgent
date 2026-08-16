import { Link, useNavigate, useParams } from 'react-router'
import { useQueries, useQuery } from '@tanstack/react-query'
import * as api from '@/shared/api'
import { caseStatusMeta, priorityMeta, taskStatusMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card, CardTitle } from '@/shared/ui/Card'
import { PageHeader } from '@/shared/ui/PageHeader'
import { PageLoading } from '@/shared/ui/Spinner'
import { openCaseWorkspace } from '@/features/workbench/workspace-store'

function InfoRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-6 py-2">
      <span className="shrink-0 text-[13px] text-ink-48">{label}</span>
      <span className="text-right text-[13px] text-ink">{value}</span>
    </div>
  )
}

export function CaseDetailPage() {
  const { caseId = '' } = useParams()
  const navigate = useNavigate()

  const extCase = useQuery({
    queryKey: ['external-case', caseId],
    queryFn: () => api.getExternalCase(caseId),
  })
  const recentEntries = api.getRecentTasks().filter((entry) => entry.externalCaseId === caseId)
  const recentTaskQueries = useQueries({
    queries: recentEntries.map((entry) => ({
      queryKey: ['task', entry.taskId],
      queryFn: () => api.getTask(entry.taskId),
      refetchInterval: (query: { state: { data?: { status: string } } }) => {
        const current = query.state.data
        return current && ['pending', 'running', 'cancel_requested'].includes(current.status) ? 5000 : false
      },
      retry: false,
    })),
  })

  if (extCase.isPending) return <PageLoading />
  if (extCase.isError || !extCase.data) {
    return <p className="py-24 text-center text-ink-48">工单不存在或无权访问</p>
  }
  const c = extCase.data
  const caseTasks = recentTaskQueries.flatMap((query) => (query.data ? [query.data] : []))

  return (
    <div>
      <Link to="/cases" className="press mb-4 inline-block text-[13px] text-primary">
        ‹ 返回工单列表
      </Link>
      <PageHeader
        eyebrow={c.externalCaseKey}
        title={c.title}
        subtitle={
          <span className="flex items-center gap-2">
            <Badge tone={caseStatusMeta[c.status].tone}>
              {caseStatusMeta[c.status].label}
            </Badge>
            <Badge tone={priorityMeta[c.priority].tone}>
              优先级 {priorityMeta[c.priority].label}
            </Badge>
          </span>
        }
        actions={
          <Button onClick={() => {
            const workspace = openCaseWorkspace(c.externalCaseId)
            navigate(`/workbench/${workspace.workspaceId}`)
          }}>
            在工作台打开
          </Button>
        }
      />

      <div className="grid gap-5 lg:grid-cols-3">
        <Card className="p-6 lg:col-span-2">
          <CardTitle className="mb-3">问题描述</CardTitle>
          {/* 阅读态：17px 规范阅读节奏 */}
          <p className="reading whitespace-pre-wrap">{c.description}</p>
        </Card>

        <div className="flex flex-col gap-5">
          <Card className="p-6">
            <CardTitle className="mb-2">基本信息</CardTitle>
            <div className="divide-y divide-divider">
              <InfoRow label="客户" value={c.customerName || '—'} />
              <InfoRow label="产品" value={[c.productName, c.productVersion].filter(Boolean).join(' ') || '—'} />
              <InfoRow label="工单类型" value={c.caseType || '—'} />
              <InfoRow label="业务模块" value={c.module || c.category || '—'} />
              <InfoRow label="上报时间" value={fmtDateTime(c.reportedAt)} />
              <InfoRow label="来源更新" value={fmtDateTime(c.sourceUpdatedAt)} />
              <InfoRow
                label="来源指纹"
                value={
                  <code className="text-[12px] text-ink-48">
                    {shortId(c.sourceFingerprint.replace('sha256:', ''))}
                  </code>
                }
              />
            </div>
          </Card>

          <Card className="p-6">
            <CardTitle className="mb-1">最近任务入口</CardTitle>
            <p className="mb-3 text-[12px] leading-[1.5] text-ink-48">
              仅记录本浏览器会话创建的任务，状态每次从后端重新读取。
            </p>
            {caseTasks.length === 0 ? (
              <p className="text-[13px] text-ink-48">本会话没有该工单的任务记录</p>
            ) : (
              <ul className="flex flex-col gap-2.5">
                {caseTasks.map((t) => (
                  <li key={t.taskId}>
                    <Link
                      to={`/tasks/${t.taskId}`}
                      className="press flex items-center justify-between gap-3 rounded-capsule bg-pearl px-3.5 py-2.5 hover:bg-parchment"
                    >
                      <span className="text-[13px] text-ink">
                        {fmtDateTime(t.createdAt)}
                      </span>
                      <Badge tone={taskStatusMeta[t.status].tone}>
                        {taskStatusMeta[t.status].label}
                      </Badge>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      </div>
    </div>
  )
}
