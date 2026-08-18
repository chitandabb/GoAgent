import { request } from './client'
import type {
  KnowledgeCitationData,
  KnowledgeDocumentListData,
  KnowledgeIngestionResponse,
  KnowledgeIngestionTask,
} from './m1-types'

/** 分页查询企业知识库文档列表（global 范围，按创建时间倒序）。管理员专用。 */
export function listKnowledgeDocuments(page = 1, pageSize = 20): Promise<KnowledgeDocumentListData> {
  return request<KnowledgeDocumentListData>(
    `/api/v1/admin/knowledge-ingestion-tasks?page=${page}&pageSize=${pageSize}`,
  )
}

/** 上传企业知识文档（首个版本）。multipart: file + title（可选）。 */
export function createKnowledgeDocument(
  file: File,
  title: string,
  idempotencyKey: string,
): Promise<KnowledgeIngestionResponse> {
  const form = new FormData()
  form.append('file', file)
  if (title.trim()) form.append('title', title.trim())
  return request<KnowledgeIngestionResponse>('/api/v1/admin/knowledge-documents', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    body: form,
  })
}

/** 为已有文档上传不可变新版本。 */
export function createKnowledgeDocumentVersion(
  documentId: string,
  file: File,
  idempotencyKey: string,
): Promise<KnowledgeIngestionResponse> {
  const form = new FormData()
  form.append('file', file)
  return request<KnowledgeIngestionResponse>(
    `/api/v1/admin/knowledge-documents/${documentId}/versions`,
    {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body: form,
    },
  )
}

export function getKnowledgeIngestionTask(taskId: string): Promise<KnowledgeIngestionTask> {
  return request<KnowledgeIngestionTask>(`/api/v1/admin/knowledge-ingestion-tasks/${taskId}`)
}

export function cancelKnowledgeIngestionTask(taskId: string): Promise<KnowledgeIngestionTask> {
  return request<KnowledgeIngestionTask>(`/api/v1/admin/knowledge-ingestion-tasks/${taskId}/cancel`, {
    method: 'POST',
  })
}

export function getKnowledgeCitation(chunkId: string): Promise<KnowledgeCitationData> {
  return request<KnowledgeCitationData>(`/api/v1/knowledge-citations/${chunkId}`)
}
