// M1 诊断闭环与会话域使用真实后端。后端尚未实现的 M2/管理域保留显式 Mock，
// 避免将整套 Mock 误当作业务 API 边界。
export {
  getDependencies,
  getSystemStats,
  listAdminDataSources,
  listCaseCards,
  listCatalogEntries,
  listCatalogVersions,
  listDeadLetters,
  publishCatalogVersion,
  requeueDeadLetter,
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
export { getRecentTasks, rememberRecentTask } from './recent-tasks'
export { subscribeTaskEvents } from './task-events'
export { subscribeTurnEvents } from './conversation-events'
export { uploadConversationAttachment, getAttachmentPreview } from './attachments'
export {
  createKnowledgeDocument,
  createKnowledgeDocumentVersion,
  getKnowledgeIngestionTask,
  cancelKnowledgeIngestionTask,
  getKnowledgeCitation,
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
