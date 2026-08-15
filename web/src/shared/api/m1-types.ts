export interface DataSource {
  id: string
  name: string
  type: 'sqlserver'
  environment: string
  status: 'active' | 'disabled'
}

export type CasePriority = 'high' | 'medium' | 'low'
export type CaseStatus = 'open' | 'processing' | 'closed'

export interface ExternalAttachment {
  externalAttachmentKey: string
  fileName: string
  mediaType: string
  sizeBytes: number
  contentHash: string
  sourceUpdatedAt: string
}

export interface ExternalCase {
  externalCaseId: string
  dataSourceId: string
  externalCaseKey: string
  caseType?: string
  title: string
  description: string
  category?: string
  module?: string
  status: CaseStatus
  priority: CasePriority
  occurredAt?: string | null
  reportedAt: string
  sourceUpdatedAt: string
  customerCode?: string
  customerName?: string
  productCode?: string
  productName?: string
  productVersion?: string
  workOrderNo?: string
  workpieceNo?: string
  materialCode?: string
  batchNo?: string
  serialNo?: string
  factoryCode?: string
  workshopCode?: string
  productionLineCode?: string
  workstationCode?: string
  equipmentCode?: string
  sourceSystem?: string
  deploymentEnvironment?: string
  businessDatabaseAlias?: string
  attributes: Record<string, unknown>
  attributesSchemaVersion: 1
  attachments: ExternalAttachment[]
  sourceFingerprint: string
  truncated: boolean
}

export interface ExternalCaseListData {
  items: ExternalCase[]
  page: number
  pageSize: number
  total: number
}

export interface CreateDiagnosisTaskInput {
  externalCaseId: string
  expectedSourceFingerprint: string
  evidenceDataSourceIds?: string[]
  requestText: string
  attachments?: never[]
  retryOfTaskId?: string | null
}

export interface DiagnosisTaskCreateData {
  taskId: string
  status: 'pending'
  replayed: boolean
  createdAt: string
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
  caseSnapshotId: string
  retryOfTaskId?: string
  requestText: string
  status: TaskStatus
  attemptCount: number
  lastErrorCode?: string
  lastErrorMessage?: string
  startedAt: string | null
  completedAt: string | null
  createdAt: string
  updatedAt: string
  reportAvailable: boolean
  reportId?: string
}

export type TaskEventType =
  | 'task_created'
  | 'task_cancel_requested'
  | 'task_started'
  | 'task_reclaimed'
  | 'task_retry_scheduled'
  | 'task_succeeded'
  | 'task_failed'
  | 'task_cancelled'
  | 'task_requeued'

export interface TaskEvent {
  seq: number
  eventType: TaskEventType | string
  payload: Record<string, unknown>
  payloadSchemaVersion: number
  createdAt: string
}

export interface TaskEventsData {
  items: TaskEvent[]
  afterSeq: number
  nextAfterSeq: number
  hasMore: boolean
}

export type SseConnectionState =
  | 'loading-history'
  | 'connected'
  | 'reconnecting'
  | 'failed'
  | 'closed'

export type ConclusionStatus = 'conclusive' | 'probable' | 'inconclusive'
export type RiskLevel = 'low' | 'medium' | 'high'
export type ReportConfidence = 'high' | 'medium' | 'low'
export type ReviewVerdict = 'adopted' | 'partially_adopted' | 'rejected'

export type EvidenceSourceType =
  | 'case_snapshot'
  | 'schema_catalog'
  | 'sql_object_definition'
  | 'sql_query'
  | 'code_search'
  | 'attachment'
  | 'knowledge_chunk'
  | 'web'

export interface DiagnosisReportUsage {
  modelCalls: number
  promptTokens: number
  completionTokens: number
  totalTokens: number
  cachedTokens: number
  reasoningTokens: number
}

export interface DiagnosisReportEvidence {
  evidenceId: string
  claimKey: string
  claim: string
  supportType: 'supports' | 'contradicts' | 'context'
  sourceType: EvidenceSourceType
  sourceRef: string
  sourceTool: string
  location?: string
  contentHash: string
  collectedAt: string
  redactionStatus: 'not_required' | 'redacted'
  truncated: boolean
  validityStatus: 'valid' | 'superseded' | 'invalid'
}

export interface DiagnosisReport {
  reportId: string
  taskId: string
  conclusionStatus: ConclusionStatus
  riskLevel: RiskLevel
  conclusion: string
  businessSummary: string
  technicalSummary: string
  confidence: ReportConfidence
  limitations: string[]
  partial: boolean
  missingEvidence: string[]
  usage: DiagnosisReportUsage
  agentRuns: number
  selectedSkill: string
  executedSkills: string[]
  stopReason?: string
  reportSchemaVersion: 1
  modelProvider: string
  modelId: string
  promptVersion: string
  evidence: DiagnosisReportEvidence[]
  generatedAt: string
  createdAt: string
  updatedAt: string
}

export interface ReportReview {
  id: string
  reportId: string
  reviewedBy: string
  verdict: ReviewVerdict
  comment: string
  createdAt: string
}

export interface ReportReviewsData {
  reportId: string
  current: ReportReview | null
  items: ReportReview[]
}

export interface DiagnosisTaskRecovery {
  recoveryId: string
  taskId: string
  status: 'pending'
  replayed: boolean
  taskEventSeq: number
  previousAttemptCount: number
  recoveredAt: string
}

export interface CaseQuery {
  dataSourceId: string
  keyword?: string
  status?: CaseStatus
  priority?: CasePriority
  caseType?: string
  reportedFrom?: string
  reportedTo?: string
  page?: number
  pageSize?: number
  sortBy?: 'reportedAt' | 'sourceUpdatedAt' | 'externalCaseKey'
  sortOrder?: 'asc' | 'desc'
}
