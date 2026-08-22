import { useNavigate } from 'react-router'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { EmptyState } from '@/shared/ui/EmptyState'
import { PageHeader } from '@/shared/ui/PageHeader'

export function SystemPage() {
  const navigate = useNavigate()
  return (
    <div>
      <PageHeader
        title="运行状态"
        subtitle="查看排查任务与系统运行情况"
      />
      <Card>
        <EmptyState
          title="集中运行监控暂不可用"
          description="你仍可在排查任务中查看每项任务的处理状态、完成时间和失败原因。"
          action={<Button onClick={() => navigate('/tasks')}>查看排查任务</Button>}
        />
      </Card>
    </div>
  )
}
