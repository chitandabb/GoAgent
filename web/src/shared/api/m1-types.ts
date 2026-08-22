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

export interface DiagnosisTaskAttachment {
  attachmentId: string
  sourceMessageId: string
  purpose: string
  originalName: string
  mediaType: string
  sizeBytes: number
  contentSha256: string
}

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
  attachments: DiagnosisTaskAttachment[]
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

// ---------------------------------------------------------------- 任务列表

export interface DiagnosisTaskListItem {
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
  externalCaseKey: string
  externalCaseTitle: string
}

export interface DiagnosisTaskListData {
  items: DiagnosisTaskListItem[]
  page: number
  pageSize: number
  total: number
}

export interface DiagnosisTaskListQuery {
  status?: TaskStatus
  /** 按创建时冻结的外部工单 ID 过滤（UUID） */
  caseId?: string
  page?: number
  pageSize?: number
}

// ---------------------------------------------------------------- 会话

export type ConversationStatus = 'active' | 'archived'

export interface ConversationSummary {
  id: string
  title: string
  firstUserMessage?: string
  status: ConversationStatus
  createdAt: string
  updatedAt: string
  lastMessageAt?: string
}

export interface ConversationListData {
  items: ConversationSummary[]
  page: number
  pageSize: number
  total: number
}

export type MessageRole = 'user' | 'assistant'

export interface ConversationCaseReference {
  externalCaseId: string
  kind: 'selected' | 'mentioned'
}

export interface ConversationTaskReference {
  taskId: string
  kind: 'created' | 'referenced'
}

export interface ConversationMessageAttachment {
  attachmentId: string
  position: number
  purpose?: string
  originalName: string
  mediaType: string
  sizeBytes: number
  contentSha256: string
  status: string
}

export interface ConversationCitation {
  position: number
  sourceType: 'attachment' | 'knowledge_chunk' | 'web'
  sourceRef: string
  contentSha256: string
}

export type ConversationSourceType = ConversationCitation['sourceType']

export interface ConversationAnswerSourceCount {
  sourceType: ConversationSourceType
  count: number
}

export interface ConversationAnswerProvenance {
  executionPath: 'agent' | 'semantic_cache_hit'
  cacheLayer?: 'exact' | 'semantic'
  outcome: 'answered' | 'insufficient_evidence' | 'degraded' | 'failed'
  toolCalls: number
  durationMillis: number
  sources: ConversationAnswerSourceCount[]
}

export interface ConversationMessage {
  id: string
  conversationId: string
  seq: number
  role: MessageRole
  content: string
  contentSchemaVersion: number
  caseReferences: ConversationCaseReference[]
  taskReferences: ConversationTaskReference[]
  attachments: ConversationMessageAttachment[]
  citations: ConversationCitation[]
  turnId?: string
  provenance?: ConversationAnswerProvenance
  createdAt: string
}

export interface ConversationMessagesData {
  items: ConversationMessage[]
  afterSeq: number
  nextAfterSeq: number
  hasMore: boolean
}

export type TurnStatus =
  | 'queued'
  | 'running'
  | 'completed'
  | 'failed'

export interface ConversationTurnResponse {
  turnId: string
  status: TurnStatus
  userMessage: ConversationMessage
  assistantMessage?: ConversationMessage
  replayed: boolean
}

export interface TurnDetail {
  turnId: string
  conversationId: string
  status: TurnStatus
  userMessageId: string
  assistantMessageId?: string
  attemptCount: number
  failureSummary?: string
  retryAt?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
}

export type TurnEventType =
  | 'turn_queued'
  | 'turn_running'
  | 'turn_retry_scheduled'
  | 'turn_tool_started'
  | 'turn_tool_completed'
  | 'turn_message_delta'
  | 'turn_completed'
  | 'turn_failed'

/** turn_message_delta 事件负载：按 position 升序拼接 content 还原完整回答。 */
export interface TurnMessageDeltaPayload {
  messageId: string
  position: number
  content: string
}

export function parseTurnMessageDelta(payload: Record<string, unknown>): TurnMessageDeltaPayload | null {
  const messageId = typeof payload.messageId === 'string' ? payload.messageId : ''
  const position = typeof payload.position === 'number' ? payload.position : Number.NaN
  const content = typeof payload.content === 'string' ? payload.content : ''
  if (!messageId || !Number.isInteger(position) || position < 0 || content.length === 0) return null
  return { messageId, position, content }
}

export interface TurnToolActivity {
  activityId: string
  toolName: string
  displayName: string
  status: 'running' | 'succeeded' | 'failed'
  inputSummary: string
  outputSummary: string
  durationMillis: number
  attemptCount: number
}

export function parseTurnToolActivity(payload: Record<string, unknown>): TurnToolActivity | null {
  const activityId = typeof payload.activityId === 'string' ? payload.activityId : ''
  const toolName = typeof payload.toolName === 'string' ? payload.toolName : ''
  const displayName = typeof payload.displayName === 'string' ? payload.displayName : ''
  const status = payload.status
  const inputSummary = typeof payload.inputSummary === 'string' ? payload.inputSummary : ''
  const outputSummary = typeof payload.outputSummary === 'string' ? payload.outputSummary : ''
  const durationMillis = typeof payload.durationMillis === 'number' ? payload.durationMillis : 0
  const attemptCount = typeof payload.attemptCount === 'number' ? payload.attemptCount : 1
  if (
    !activityId || !toolName || !displayName ||
    (status !== 'running' && status !== 'succeeded' && status !== 'failed') ||
    !Number.isFinite(durationMillis) || durationMillis < 0 ||
    !Number.isInteger(attemptCount) || attemptCount < 1
  ) return null
  return { activityId, toolName, displayName, status, inputSummary, outputSummary, durationMillis, attemptCount }
}

export function parseTurnCompletedProvenance(payload: Record<string, unknown>): ConversationAnswerProvenance | null {
  const raw = payload.provenance
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const value = raw as Record<string, unknown>
  const executionPath = value.executionPath === 'semantic_cache_hit' ? 'semantic_cache_hit' : value.executionPath === 'agent' || value.executionPath === '' ? 'agent' : null
  const outcome = value.outcome
  const toolCalls = typeof value.toolCalls === 'number' ? value.toolCalls : Number.NaN
  const durationMillis = typeof value.durationMillis === 'number' ? value.durationMillis : Number.NaN
  if (
    !executionPath ||
    (outcome !== 'answered' && outcome !== 'insufficient_evidence' && outcome !== 'degraded' && outcome !== 'failed') ||
    !Number.isInteger(toolCalls) || toolCalls < 0 ||
    !Number.isFinite(durationMillis) || durationMillis < 0
  ) return null
  const sources: ConversationAnswerSourceCount[] = Array.isArray(value.sources)
    ? value.sources.flatMap((item) => {
        if (!item || typeof item !== 'object' || Array.isArray(item)) return []
        const source = item as Record<string, unknown>
        const sourceType = source.sourceType
        const count = source.count
        if (
          (sourceType !== 'attachment' && sourceType !== 'knowledge_chunk' && sourceType !== 'web') ||
          typeof count !== 'number' || !Number.isInteger(count) || count < 1
        ) return []
        return [{ sourceType, count }]
      })
    : []
  const cacheLayer = value.cacheLayer === 'exact' || value.cacheLayer === 'semantic' ? value.cacheLayer : undefined
  return { executionPath, cacheLayer, outcome, toolCalls, durationMillis, sources }
}

export interface TurnEvent {
  seq: number
  eventType: TurnEventType | string
  payload: Record<string, unknown>
  payloadSchemaVersion: number
  createdAt: string
}

export interface TurnEventsData {
  items: TurnEvent[]
  afterSeq: number
  nextAfterSeq: number
  hasMore: boolean
}

export interface ConversationAttachment {
  attachmentId: string
  conversationId: string
  scope: 'session'
  status: 'ready' | 'processing' | 'failed'
  originalName: string
  mediaType: string
  sizeBytes: number
  contentSha256: string
  replayed: boolean
  uploadedAt: string
}

export interface AttachmentPreviewElement {
  index: number
  pageNumber?: number
  elementType: string
  sectionPath?: string[]
  contentText: string
}

export interface AttachmentPreviewData {
  sourceType: 'attachment'
  sourceRef: string
  attachmentId: string
  originalName: string
  mediaType: string
  sizeBytes: number
  contentSha256: string
  parserVersion: string
  elements: AttachmentPreviewElement[]
  visualAssetCount: number
  truncated: boolean
}

export interface SendMessageInput {
  content: string
  caseReferences?: { externalCaseId: string; kind?: 'selected' | 'mentioned' }[]
  taskReferences?: { taskId: string; kind?: 'created' | 'referenced' }[]
  attachments?: { attachmentId: string; purpose?: string }[]
}

// ---------------------------------------------------------------- 知识库

export type KnowledgeScope = 'personal' | 'global'
export type IngestionTaskStatus =
  | 'pending'
  | 'running'
  | 'retry_wait'
  | 'cancel_requested'
  | 'succeeded'
  | 'partial_succeeded'
  | 'failed'
  | 'cancelled'
export type IngestionStage =
  | 'uploaded'
  | 'scanning'
  | 'parsing'
  | 'chunking'
  | 'indexing'
  | 'publishing'
  | 'completed'

/** 企业知识库文档列表行：最新版本号 + 最新解析任务状态。 */
export interface KnowledgeDocumentListItem {
  documentId: string
  title: string
  scope: KnowledgeScope
  version: number
  taskId: string
  status: IngestionTaskStatus | null
  stage: IngestionStage | null
  progressPercent: number
  createdAt: string
}

export interface KnowledgeDocumentListData {
  items: KnowledgeDocumentListItem[]
  page: number
  pageSize: number
  total: number
}

export interface KnowledgeIngestionResponse {
  documentId: string
  documentVersionId: string
  version: number
  taskId: string
  status: IngestionTaskStatus
  stage: IngestionStage
  replayed: boolean
  createdAt: string
}

export interface KnowledgeIngestionTask {
  taskId: string
  documentVersionId: string
  documentId: string
  status: IngestionTaskStatus
  stage: IngestionStage
  attemptCount: number
  maxAttempts: number
  progressPercent: number
  cancelRequestedAt?: string
  lastError?: { code: string; message: string }
  startedAt?: string
  completedAt?: string
  createdAt: string
  updatedAt: string
  cancellationChanged?: boolean
}

export interface KnowledgeCitationData {
  sourceType: string
  sourceRef: string
  documentId: string
  documentVersionId: string
  chunkId: string
  title: string
  scope: KnowledgeScope
  version: number
  ordinal: number
  pageNumber?: number
  elementType: string
  sectionPath: string[]
  contentText: string
  contentSha256: string
}

// ---------------------------------------------------------------- 管理域

export type AdminUserStatus = 'active' | 'disabled'

export interface AdminUser {
  id: string
  username: string
  displayName: string
  role: 'analyst' | 'admin'
  status: AdminUserStatus
  mustChangePassword: boolean
  lastLoginAt?: string
  createdAt: string
  updatedAt: string
}

export interface AdminUserListData {
  items: AdminUser[]
  page: number
  pageSize: number
  total: number
}

export interface AdminUserListQuery {
  status?: AdminUserStatus
  role?: 'analyst' | 'admin'
  page?: number
  pageSize?: number
}

export interface CreateAdminUserInput {
  username: string
  displayName: string
  role: 'analyst' | 'admin'
  temporaryPassword: string
}

export interface UpdateAdminUserInput {
  status?: AdminUserStatus
  role?: 'analyst' | 'admin'
}
