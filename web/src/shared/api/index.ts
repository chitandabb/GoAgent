// M1 诊断闭环使用真实后端。后端尚未实现的 M2/管理域保留显式 Mock，
// 避免将整套 Mock 误当作业务 API 边界。
export {
  createConversation,
  createUser,
  getDependencies,
  getSystemStats,
  listAdminDataSources,
  listCaseCards,
  listCatalogEntries,
  listCatalogVersions,
  listConversations,
  listDeadLetters,
  listKnowledgeDocs,
  listUsers,
  publishCatalogVersion,
  requeueDeadLetter,
  resetUserPassword,
  sendAssistantMessage,
  setUserRole,
  setUserStatus,
  startCatalogScan,
  stopGeneration,
  subscribeConversation,
  updateCatalogEntry,
  uploadKnowledgeDoc,
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
} from './business'
export { getRecentTasks, rememberRecentTask } from './recent-tasks'
export { subscribeTaskEvents } from './task-events'
export * from './types'
export * from './errors'
export { onUnauthorized } from './client'
export { changePassword, login, logout, me } from './auth'
