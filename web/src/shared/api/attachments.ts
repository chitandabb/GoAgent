import { request } from './client'
import type { AttachmentPreviewData, ConversationAttachment } from './m1-types'

export function uploadConversationAttachment(
  conversationId: string,
  file: File,
  idempotencyKey: string,
): Promise<ConversationAttachment> {
  const form = new FormData()
  form.append('file', file)
  return request<ConversationAttachment>(
    `/api/v1/conversations/${conversationId}/attachments`,
    {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body: form,
    },
  )
}

export function getAttachmentPreview(
  conversationId: string,
  attachmentId: string,
): Promise<AttachmentPreviewData> {
  return request<AttachmentPreviewData>(
    `/api/v1/conversations/${conversationId}/attachments/${attachmentId}/preview`,
  )
}
