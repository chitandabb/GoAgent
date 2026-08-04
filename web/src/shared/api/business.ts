import { request } from './client'
import type {
  CaseQuery,
  CreateDiagnosisTaskInput,
  DataSource,
  DiagnosisReport,
  DiagnosisTask,
  DiagnosisTaskCreateData,
  DiagnosisTaskRecovery,
  ExternalCase,
  ExternalCaseListData,
  ReportReviewsData,
  ReviewVerdict,
  TaskEventsData,
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
