import type {
  CasePriority,
  CaseStatus,
  ConclusionStatus,
  DocProcessingStatus,
  EvidenceSourceType,
  GenerationStatus,
  RiskLevel,
  TaskStatus,
  ReviewVerdict,
} from '@/shared/api/types'

export type Tone = 'gray' | 'blue' | 'green' | 'orange' | 'red'

interface Meta {
  label: string
  tone: Tone
}

export const taskStatusMeta: Record<TaskStatus, Meta> = {
  pending: { label: '等待执行', tone: 'gray' },
  running: { label: '执行中', tone: 'blue' },
  cancel_requested: { label: '取消中', tone: 'orange' },
  succeeded: { label: '已完成', tone: 'green' },
  failed: { label: '失败', tone: 'red' },
  cancelled: { label: '已取消', tone: 'gray' },
}

export const caseStatusMeta: Record<CaseStatus, Meta> = {
  open: { label: '待处理', tone: 'blue' },
  processing: { label: '处理中', tone: 'orange' },
  closed: { label: '已关闭', tone: 'gray' },
}

export const priorityMeta: Record<CasePriority, Meta> = {
  high: { label: '高', tone: 'red' },
  medium: { label: '中', tone: 'orange' },
  low: { label: '低', tone: 'gray' },
}

export const conclusionMeta: Record<ConclusionStatus, Meta> = {
  conclusive: { label: '结论明确', tone: 'green' },
  probable: { label: '可能性较高', tone: 'orange' },
  inconclusive: { label: '证据不足', tone: 'gray' },
}

export const riskMeta: Record<RiskLevel, Meta> = {
  low: { label: '低风险', tone: 'green' },
  medium: { label: '中风险', tone: 'orange' },
  high: { label: '高风险', tone: 'red' },
}

export const verdictMeta: Record<ReviewVerdict, Meta> = {
  adopted: { label: '已采纳', tone: 'green' },
  partially_adopted: { label: '部分采纳', tone: 'orange' },
  rejected: { label: '已驳回', tone: 'red' },
}

export const evidenceSourceMeta: Record<EvidenceSourceType, Meta> = {
  case_snapshot: { label: '工单快照', tone: 'blue' },
  sql_query: { label: '数据库查询', tone: 'blue' },
  knowledge_case: { label: '历史案例', tone: 'green' },
  attachment: { label: '附件', tone: 'gray' },
}

export const depStatusMeta: Record<'up' | 'degraded' | 'down', Meta> = {
  up: { label: '正常', tone: 'green' },
  degraded: { label: '降级', tone: 'orange' },
  down: { label: '不可用', tone: 'red' },
}

export const docStatusMeta: Record<DocProcessingStatus, Meta> = {
  uploaded: { label: '已上传', tone: 'gray' },
  processing: { label: '处理中', tone: 'blue' },
  ready: { label: '可检索', tone: 'green' },
  failed: { label: '处理失败', tone: 'red' },
}

export const generationMeta: Record<GenerationStatus, Meta> = {
  streaming: { label: '生成中', tone: 'blue' },
  completed: { label: '已完成', tone: 'green' },
  interrupted: { label: '生成已中止', tone: 'orange' },
  failed: { label: '生成失败', tone: 'red' },
}

export const catalogStatusMeta: Record<'draft' | 'published' | 'retired', Meta> = {
  draft: { label: '草稿', tone: 'orange' },
  published: { label: '已发布', tone: 'green' },
  retired: { label: '已退役', tone: 'gray' },
}

export const scanStatusMeta: Record<'succeeded' | 'running' | 'failed', Meta> = {
  succeeded: { label: '扫描成功', tone: 'green' },
  running: { label: '扫描中', tone: 'blue' },
  failed: { label: '扫描失败', tone: 'red' },
}

export const toolStatusMeta: Record<'succeeded' | 'failed' | 'timed_out', Meta> = {
  succeeded: { label: '成功', tone: 'green' },
  failed: { label: '失败', tone: 'red' },
  timed_out: { label: '超时', tone: 'orange' },
}
