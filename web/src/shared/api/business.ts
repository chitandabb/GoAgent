import { request } from './client'
import type {
  CaseQuery,
  ConversationListData,
  ConversationMessage,
  ConversationMessagesData,
  ConversationSummary,
  ConversationTurnResponse,
  CreateDiagnosisTaskInput,
  DataSource,
  DiagnosisReport,
  DiagnosisTask,
  DiagnosisTaskCreateData,
  DiagnosisTaskListData,
  DiagnosisTaskListQuery,
  DiagnosisTaskRecovery,
  ExternalCase,
  ExternalCaseListData,
  ReportReviewsData,
  ReviewVerdict,
  SendMessageInput,
  TaskEventsData,
  TurnDetail,
  TurnEventsData,
} from './m1-types'

function queryString(values: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams()
  Object.entries(values).forEach(([key, value]) => {
    if (value !== undefined && value !== '') params.set(key, String(value))
  })
  const value = params.toString()
  return value ? `?${value}` : ''
}

export function createIdempotencyKey(): string {
  return crypto.randomUUID()
}

export async function listDataSources(): Promise<DataSource[]> {
  const data = await request<{ items: DataSource[] }>('/api/v1/data-sources')
  return data.items
}

export function listExternalCases(query: CaseQuery): Promise<ExternalCaseListData> {
  return request<ExternalCaseListData>(
    `/api/v1/external-cases${queryString({ ...query })}`,
  )
}

export function getExternalCase(externalCaseId: string): Promise<ExternalCase> {
  return request<ExternalCase>(`/api/v1/external-cases/${externalCaseId}`)
}

export function createDiagnosisTask(
  input: CreateDiagnosisTaskInput,
  idempotencyKey: string,
): Promise<DiagnosisTaskCreateData> {
  return request<DiagnosisTaskCreateData>('/api/v1/diagnosis-tasks', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function getTask(taskId: string): Promise<DiagnosisTask> {
  return request<DiagnosisTask>(`/api/v1/diagnosis-tasks/${taskId}`)
}

export function listTaskEvents(
  taskId: string,
  afterSeq: number,
  limit = 100,
): Promise<TaskEventsData> {
  return request<TaskEventsData>(
    `/api/v1/diagnosis-tasks/${taskId}/events${queryString({ afterSeq, limit })}`,
    { headers: { Accept: 'application/json' } },
  )
}

export function cancelTask(taskId: string): Promise<DiagnosisTask> {
  return request<DiagnosisTask>(`/api/v1/diagnosis-tasks/${taskId}/cancel`, {
    method: 'POST',
  })
}

export function getReportByTask(taskId: string): Promise<DiagnosisReport> {
  return request<DiagnosisReport>(`/api/v1/diagnosis-tasks/${taskId}/report`)
}

export function listReportReviews(reportId: string): Promise<ReportReviewsData> {
  return request<ReportReviewsData>(`/api/v1/diagnosis-reports/${reportId}/reviews`)
}

export function submitReview(
  reportId: string,
  verdict: ReviewVerdict,
  comment: string,
): Promise<void> {
  return request<void>(`/api/v1/diagnosis-reports/${reportId}/reviews`, {
    method: 'POST',
    body: JSON.stringify({ verdict, comment: comment.trim() || undefined }),
  })
}

export function recoverTask(
  taskId: string,
  reason: string,
  idempotencyKey: string,
): Promise<DiagnosisTaskRecovery> {
  return request<DiagnosisTaskRecovery>(
    `/api/v1/admin/diagnosis-tasks/${taskId}/recover`,
    {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body: JSON.stringify({ reason: reason.trim() }),
    },
  )
}

// ---------------------------------------------------------------- 任务列表

export function listDiagnosisTasks(query: DiagnosisTaskListQuery = {}): Promise<DiagnosisTaskListData> {
  return request<DiagnosisTaskListData>(
    `/api/v1/diagnosis-tasks${queryString({ status: query.status, page: query.page, pageSize: query.pageSize })}`,
  )
}

// ---------------------------------------------------------------- AI 会话

export function listConversations(page = 1, pageSize = 50): Promise<ConversationListData> {
  return request<ConversationListData>(
    `/api/v1/conversations${queryString({ page, pageSize })}`,
  )
}

export function createConversation(title = ''): Promise<ConversationSummary> {
  return request<ConversationSummary>('/api/v1/conversations', {
    method: 'POST',
    body: JSON.stringify({ title: title.trim() }),
  })
}

export function getConversationMessages(
  conversationId: string,
  afterSeq = 0,
  limit = 100,
): Promise<ConversationMessagesData> {
  return request<ConversationMessagesData>(
    `/api/v1/conversations/${conversationId}/messages${queryString({ afterSeq, limit })}`,
  )
}

export function appendTurn(
  conversationId: string,
  input: SendMessageInput,
  idempotencyKey: string,
): Promise<ConversationTurnResponse> {
  return request<ConversationTurnResponse>(`/api/v1/conversations/${conversationId}/turns`, {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: JSON.stringify(input),
  })
}

export function getTurn(
  conversationId: string,
  turnId: string,
): Promise<TurnDetail> {
  return request<TurnDetail>(`/api/v1/conversations/${conversationId}/turns/${turnId}`)
}

export function listTurnEvents(
  conversationId: string,
  turnId: string,
  afterSeq: number,
  limit = 100,
): Promise<TurnEventsData> {
  return request<TurnEventsData>(
    `/api/v1/conversations/${conversationId}/turns/${turnId}/events${queryString({ afterSeq, limit })}`,
  )
}

export function listConversationMessages(conversationId: string): Promise<ConversationMessage[]> {
  return getConversationMessages(conversationId).then((page) => page.items)
}
