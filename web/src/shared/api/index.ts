// M1 诊断闭环、会话域、知识库与用户管理均使用真实后端。后端尚未实现的
// Schema Catalog 管理域保留显式 Mock，避免将 Mock 误当作业务 API 边界。
export {
  listAdminDataSources,
  listCatalogEntries,
  listCatalogVersions,
  publishCatalogVersion,
  startCatalogScan,
  updateCatalogEntry,
} from '@/mocks/api'
export {
  cancelTask,
  createDiagnosisTask,
  createIdempotencyKey,
  getExternalCase,
  getReportByTask,
  getTask,
  listDataSources,
  listExternalCases,
  listReportReviews,
  listTaskEvents,
  recoverTask,
  submitReview,
  listDiagnosisTasks,
  listConversations,
  createConversation,
  getConversationMessages,
  listConversationMessages,
  appendTurn,
  getTurn,
  listTurnEvents,
} from './business'
export { subscribeTaskEvents } from './task-events'
export { subscribeTurnEvents } from './conversation-events'
export { uploadConversationAttachment, getAttachmentPreview } from './attachments'
export {
  createKnowledgeDocument,
  createKnowledgeDocumentVersion,
  getKnowledgeIngestionTask,
  cancelKnowledgeIngestionTask,
  getKnowledgeCitation,
  listKnowledgeDocuments,
} from './knowledge'
export {
  listAdminUsers,
  createAdminUser,
  updateAdminUser,
  resetAdminUserPassword,
} from './admin-users'
export * from './types'
export * from './errors'
export { onUnauthorized } from './client'
export { changePassword, login, logout, me } from './auth'
