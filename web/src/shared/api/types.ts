// 与 docs/design/api.md、domain-and-state-machine.md 对齐的前端类型。
// 后端 OpenAPI 建立后，由 openapi-typescript 生成的类型替换本文件。

export type Role = 'analyst' | 'admin'

export interface CurrentUser {
  id: string
  username: string
  displayName: string
  role: Role
  /** true 时仅允许修改密码与退出（api.md 认证规则） */
  mustChangePassword: boolean
}

export interface DataSource {
  id: string
  name: string
  type: 'sqlserver'
  status: 'active' | 'disabled'
  environment: string
}

export type CasePriority = 'high' | 'medium' | 'low'
export type CaseStatus = 'open' | 'processing' | 'closed'

export interface ExternalCase {
  externalCaseId: string
  dataSourceId: string
  externalCaseKey: string
  title: string
  description: string
  status: CaseStatus
  priority: CasePriority
  customerName: string
  productName: string
  productVersion: string
  reportedAt: string
  sourceUpdatedAt: string
  sourceFingerprint: string
}

export type TaskStatus =
  | 'pending'
  | 'running'
  | 'cancel_requested'
  | 'succeeded'
  | 'failed'
  | 'cancelled'

export interface DiagnosisTask {
  taskId: string
  externalCaseId: string
  externalCaseKey: string
  caseTitle: string
  status: TaskStatus
  createdBy: string
  createdAt: string
  completedAt: string | null
  dataSourceNames: string[]
  requestText: string
  attachmentNames: string[]
  reportId: string | null
  errorMessage: string | null
  /** 重新诊断时指向原任务；始终创建新任务，不覆盖历史 */
  retryOfTaskId: string | null
}

export type TaskEventType =
  | 'task_created'
  | 'task_started'
  | 'step_started'
  | 'tool_succeeded'
  | 'step_completed'
  | 'report_generated'
  | 'task_succeeded'
  | 'task_failed'
  | 'cancel_requested'
  | 'task_cancelled'
  | 'task_requeued'

export interface TaskEvent {
  taskId: string
  seq: number
  type: TaskEventType
  occurredAt: string
  title: string
  detail?: string
}

export type ConclusionStatus = 'conclusive' | 'probable' | 'inconclusive'
export type RiskLevel = 'low' | 'medium' | 'high'
export type ReviewVerdict = 'adopted' | 'partially_adopted' | 'rejected'

export type EvidenceSourceType =
  | 'case_snapshot'
  | 'sql_query'
  | 'knowledge_case'
  | 'attachment'

export interface EvidenceItem {
  evidenceId: string
  sourceType: EvidenceSourceType
  summary: string
  location: string
  collectedAt: string
}

export interface ToolExecution {
  executionId: string
  taskId: string
  stepName: string
  toolName: string
  status: 'succeeded' | 'failed' | 'timed_out'
  startedAt: string
  durationMs: number
  rowCount?: number
  truncated?: boolean
  /** 仅 admin 可见 */
  tokens?: number
  /** 仅 admin 可见 */
  costText?: string
}

export interface ReportClaim {
  claimId: string
  statement: string
  evidenceIds: string[]
}

export interface ReportReview {
  reviewId: string
  verdict: ReviewVerdict
  comment: string
  reviewedBy: string
  reviewedAt: string
}

export interface DiagnosisReport {
  reportId: string
  taskId: string
  conclusionStatus: ConclusionStatus
  riskLevel: RiskLevel
  business: {
    overview: string
    likelyCause: string
    impact: string
    suggestion: string
    needDeveloper: boolean
  }
  claims: ReportClaim[]
  evidence: EvidenceItem[]
  limitations: string[]
  /** conclusionStatus=inconclusive 时必填：已检查/缺少什么/下一步建议 */
  inconclusiveDetail?: {
    checked: string[]
    missing: string[]
    nextSteps: string[]
  }
  generatedAt: string
  modelVersion: string
  reviews: ReportReview[]
}

export interface CreateDiagnosisInput {
  externalCaseId: string
  /** 用户确认时的工单指纹；服务端复核不一致返回 40923 */
  expectedSourceFingerprint: string
  evidenceDataSourceIds: string[]
  requestText: string
  timeFrom?: string
  timeTo?: string
  attachmentNames: string[]
  retryOfTaskId?: string | null
}

// ---------------------------------------------------------------- 知识助手（M2）

export type MessageRole = 'user' | 'assistant'
export type GenerationStatus = 'streaming' | 'completed' | 'interrupted' | 'failed'
export type AttachmentScope = 'session' | 'personal'

export interface MessageSource {
  kind: 'knowledge' | 'web'
  title: string
  location: string
  url?: string
  retrievedAt?: string
}

export interface MessageAttachment {
  name: string
  scope: AttachmentScope
}

export interface ChatMessage {
  messageId: string
  conversationId: string
  role: MessageRole
  content: string
  status: GenerationStatus
  createdAt: string
  attachments: MessageAttachment[]
  sources: MessageSource[]
}

export interface Conversation {
  conversationId: string
  title: string
  updatedAt: string
  messageCount: number
}

// ---------------------------------------------------------------- 知识库（M2）

export type DocProcessingStatus = 'uploaded' | 'processing' | 'ready' | 'failed'

export interface KnowledgeDoc {
  documentId: string
  title: string
  scope: 'global' | 'personal'
  fileType: string
  sizeBytes: number
  status: DocProcessingStatus
  chunkCount: number
  /** 混合解析元素概览，如 “文本 42 · 表格 3 · 截图 5” */
  elementSummary?: string
  owner: string
  updatedAt: string
  failReason?: string
}

export interface CaseCard {
  cardId: string
  title: string
  productName: string
  productVersion: string
  environment: string
  symptom: string
  rootCause: string
  solution: string
  verification: string
  notApplicable: string
  updatedAt: string
}

// ---------------------------------------------------------------- Schema Catalog(admin)

export interface AdminDataSource extends DataSource {
  lastCheckStatus: 'up' | 'down'
  lastCheckAt: string
  publishedCatalogVersion: number | null
  lastScanStatus: 'succeeded' | 'running' | 'failed' | null
}

export interface CatalogVersion {
  versionId: string
  dataSourceId: string
  version: number
  status: 'draft' | 'published' | 'retired'
  scanStatus: 'succeeded' | 'running' | 'failed'
  entryCount: number
  createdBy: string
  createdAt: string
  publishedAt: string | null
}

export type SensitivityLevel = 'public' | 'internal' | 'sensitive'

export interface CatalogEntry {
  entryId: string
  versionId: string
  schemaName: string
  objectName: string
  columnName: string
  dataType: string
  comment: string
  semanticAliases: string[]
  queryable: boolean
  sensitivityLevel: SensitivityLevel
}

// ---------------------------------------------------------------- 管理

export interface AdminUser {
  id: string
  username: string
  displayName: string
  role: Role
  status: 'active' | 'disabled'
  mustChangePassword: boolean
  createdAt: string
  lastLoginAt: string | null
}

export interface CreateUserInput {
  username: string
  displayName: string
  role: Role
  temporaryPassword: string
}

export interface DependencyStatus {
  name: string
  kind: string
  status: 'up' | 'degraded' | 'down'
  latencyMs: number
  checkedAt: string
  message?: string
}

export interface SystemStats {
  queueBacklog: number
  outboxUnpublished: number
  failedTasks24h: number
  runningTasks: number
}

export type SseConnectionState = 'connected' | 'reconnecting' | 'polling'

export interface DeadLetterMessage {
  messageId: string
  queue: string
  reason: string
  attempts: number
  occurredAt: string
  taskId: string | null
}
