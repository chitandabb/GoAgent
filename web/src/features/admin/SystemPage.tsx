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
        title="系统状态"
        subtitle="当前后端尚未提供依赖状态、队列统计、失败任务列表和死信队列接口"
      />
      <Card>
        <EmptyState
          title="管理监控尚未接入"
          description="本页不再用 Mock 数据冒充真实系统状态。已知 failed taskId 可从最近任务页打开；满足后端条件时在任务详情填写原因恢复。"
          action={<Button onClick={() => navigate('/tasks')}>打开最近任务入口</Button>}
        />
      </Card>
    </div>
  )
}
