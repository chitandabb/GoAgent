import { Link, useNavigate, useParams } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import * as api from '@/shared/api'
import { caseStatusMeta, priorityMeta, taskStatusMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card, CardTitle } from '@/shared/ui/Card'
import { PageHeader } from '@/shared/ui/PageHeader'
import { PageLoading } from '@/shared/ui/Spinner'

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
  const tasks = useQuery({
    queryKey: ['tasks', 'all'],
    queryFn: () => api.listTasks({}),
  })

  if (extCase.isPending) return <PageLoading />
  if (extCase.isError || !extCase.data) {
    return <p className="py-24 text-center text-ink-48">工单不存在或无权访问</p>
  }
  const c = extCase.data
  const caseTasks = (tasks.data ?? []).filter((t) => t.externalCaseId === c.externalCaseId)

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
          <Button onClick={() => navigate(`/cases/${c.externalCaseId}/diagnose`)}>
            发起诊断
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
              <InfoRow label="客户" value={c.customerName} />
              <InfoRow label="产品" value={`${c.productName} ${c.productVersion}`} />
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
            <CardTitle className="mb-3">历史诊断</CardTitle>
            {caseTasks.length === 0 ? (
              <p className="text-[13px] text-ink-48">该工单尚未发起过诊断</p>
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
