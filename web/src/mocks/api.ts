// 模拟 API：内存存储 + 假延迟 + 模拟 SSE/流式生成。接入真实后端后整个 mocks
// 目录删除，并把 src/shared/api/index.ts 换成 fetch 实现。
import type {
  AdminDataSource,
  AdminUser,
  CaseCard,
  CatalogEntry,
  CatalogVersion,
  ChatMessage,
  Conversation,
  CreateDiagnosisInput,
  CreateUserInput,
  CurrentUser,
  DataSource,
  DeadLetterMessage,
  DependencyStatus,
  DiagnosisReport,
  DiagnosisTask,
  EvidenceItem,
  ExternalCase,
  KnowledgeDoc,
  MessageAttachment,
  ReportReview,
  ReviewVerdict,
  SseConnectionState,
  SystemStats,
  TaskEvent,
  ToolExecution,
} from '@/shared/api/types'
import { ApiError } from '@/shared/api/errors'
import {
  iso,
  mockAccounts,
  mockAdminUsers,
  mockCases,
  mockDataSources,
  mockDependencies,
  mockSystemStats,
} from './data'
import {
  buildEvidence,
  buildReport,
  buildToolExecution,
  liveScript,
  materializeEvents,
} from './scenario'
import { pickAnswer, seedConversations, seedMessages } from './assistant'
import {
  seedAdminDataSources,
  seedCaseCards,
  seedCatalogEntries,
  seedCatalogVersions,
  seedKnowledgeDocs,
} from './knowledge'

const sleep = (ms = 250) => new Promise((r) => setTimeout(r, ms))
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v))

// ---------------------------------------------------------------- 内存存储

interface Store {
  tasks: DiagnosisTask[]
  events: Record<string, TaskEvent[]>
  evidence: Record<string, EvidenceItem[]>
  toolExecutions: Record<string, ToolExecution[]>
  reports: Record<string, DiagnosisReport>
  conversations: Conversation[]
  messages: Record<string, ChatMessage[]>
  knowledgeDocs: KnowledgeDoc[]
  caseCards: CaseCard[]
  adminUsers: AdminUser[]
  adminDataSources: AdminDataSource[]
  catalogVersions: CatalogVersion[]
  catalogEntries: CatalogEntry[]
}

const HOUR = 3_600_000
const DAY = 24 * HOUR

function seedTask(
  taskId: string,
  caseId: string,
  startMsAgo: number,
  outcome: 'succeeded' | 'failed' | 'cancelled',
): {
  task: DiagnosisTask
  events: TaskEvent[]
  evidence: EvidenceItem[]
  toolExecutions: ToolExecution[]
  report?: DiagnosisReport
} {
  const extCase = mockCases.find((c) => c.externalCaseId === caseId)!
  const startAt = Date.now() - startMsAgo

  let script = liveScript
  if (outcome === 'failed') script = liveScript.slice(0, 8)
  if (outcome === 'cancelled') script = liveScript.slice(0, 5)

  const raw = materializeEvents(startAt, script)
  if (outcome === 'failed') {
    const last = raw[raw.length - 1]
    const t = new Date(last.occurredAt).getTime()
    raw.push({
      seq: raw.length + 1,
      type: 'task_failed',
      title: '诊断失败',
      detail: '模型服务连续超时，已重试 3 次后停止',
      occurredAt: new Date(t + 4000).toISOString(),
    })
  }
  if (outcome === 'cancelled') {
    const last = raw[raw.length - 1]
    const t = new Date(last.occurredAt).getTime()
    raw.push(
      {
        seq: raw.length + 1,
        type: 'cancel_requested',
        title: '收到取消请求',
        detail: '由任务创建者发起',
        occurredAt: new Date(t + 2000).toISOString(),
      },
      {
        seq: raw.length + 2,
        type: 'task_cancelled',
        title: '任务已取消',
        detail: '已完成的步骤与证据保留，可用于审计',
        occurredAt: new Date(t + 3200).toISOString(),
      },
    )
  }

  const events: TaskEvent[] = raw.map((e) => ({
    taskId,
    seq: e.seq,
    type: e.type,
    occurredAt: e.occurredAt,
    title: e.title,
    detail: e.detail,
  }))
  const evidence: EvidenceItem[] = []
  const toolExecutions: ToolExecution[] = []
  for (const e of raw) {
    if (e.evidence) evidence.push(buildEvidence(taskId, e.evidence, e.occurredAt))
    if (e.tool) toolExecutions.push(buildToolExecution(taskId, e.tool, e.occurredAt))
  }

  const endAt = events[events.length - 1].occurredAt
  const reportId = outcome === 'succeeded' ? `rpt-${taskId}` : null

  const task: DiagnosisTask = {
    taskId,
    externalCaseId: extCase.externalCaseId,
    externalCaseKey: extCase.externalCaseKey,
    caseTitle: extCase.title,
    status: outcome,
    createdBy: 'analyst01',
    createdAt: new Date(startAt).toISOString(),
    completedAt: endAt,
    dataSourceNames: ['MES 生产库（演示）'],
    requestText:
      outcome === 'succeeded'
        ? '请优先检查数据库中批次关联数据是否完整'
        : '请分析该问题的可能原因',
    attachmentNames: outcome === 'succeeded' ? ['现场截图-补打条码记录.png'] : [],
    reportId,
    errorMessage:
      outcome === 'failed' ? '模型服务连续超时，已重试 3 次后停止' : null,
    retryOfTaskId: null,
  }

  let report: DiagnosisReport | undefined
  if (reportId) {
    report = buildReport(taskId, reportId, extCase, endAt)
    report.reviews.push({
      reviewId: `rv-${taskId}-1`,
      verdict: 'partially_adopted',
      comment: '数据库方向正确，补齐关联字段后追溯已恢复；写入逻辑仍需开发确认。',
      reviewedBy: '张若楠',
      reviewedAt: iso(DAY),
    })
  }

  return { task, events, evidence, toolExecutions, report }
}

function initStore(): Store {
  const s1 = seedTask('task-demo-a1', 'ec-00098', 2 * DAY, 'succeeded')
  const s2 = seedTask('task-demo-b2', 'ec-00107', 22 * HOUR, 'failed')
  const s3 = seedTask('task-demo-c3', 'ec-00119', 5 * HOUR, 'cancelled')
  const store: Store = {
    tasks: [],
    events: {},
    evidence: {},
    toolExecutions: {},
    reports: {},
    conversations: clone(seedConversations),
    messages: clone(seedMessages),
    knowledgeDocs: clone(seedKnowledgeDocs),
    caseCards: clone(seedCaseCards),
    adminUsers: clone(mockAdminUsers),
    adminDataSources: clone(seedAdminDataSources),
    catalogVersions: clone(seedCatalogVersions),
    catalogEntries: clone(seedCatalogEntries),
  }
  for (const s of [s1, s2, s3]) {
    store.tasks.push(s.task)
    store.events[s.task.taskId] = s.events
    store.evidence[s.task.taskId] = s.evidence
    store.toolExecutions[s.task.taskId] = s.toolExecutions
    if (s.report) store.reports[s.report.reportId] = s.report
  }
  return store
}

const store = initStore()

// ---------------------------------------------------------------- 认证

const SESSION_KEY = 'mesguard.mock.session'

export async function login(
  username: string,
  password: string,
): Promise<CurrentUser> {
  await sleep(400)
  const account = mockAccounts.find((a) => a.username === username.trim())
  if (!account || !password) {
    throw new ApiError(40101, '用户名或密码错误')
  }
  const user: CurrentUser = {
    id: account.id,
    username: account.username,
    displayName: account.displayName,
    role: account.role,
    mustChangePassword: account.mustChangePassword,
  }
  sessionStorage.setItem(SESSION_KEY, JSON.stringify(user))
  return user
}

/**
 * 修改自己的密码：成功后撤销全部旧 Session（api.md），需要重新登录。
 * 演示实现不校验旧密码内容，只要求非空。
 */
export async function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<void> {
  await sleep(400)
  if (!currentPassword) throw new ApiError(42201, '请输入当前密码')
  if (newPassword.length < 8) throw new ApiError(42201, '新密码至少 8 位')
  const user = currentUser()
  const account = mockAccounts.find((a) => a.id === user.id)
  if (account) account.mustChangePassword = false
  const adminUser = store.adminUsers.find((u) => u.id === user.id)
  if (adminUser) adminUser.mustChangePassword = false
  // 撤销当前会话
  sessionStorage.removeItem(SESSION_KEY)
}

export async function me(): Promise<CurrentUser | null> {
  await sleep(120)
  const raw = sessionStorage.getItem(SESSION_KEY)
  return raw ? (JSON.parse(raw) as CurrentUser) : null
}

export async function logout(): Promise<void> {
  await sleep(120)
  sessionStorage.removeItem(SESSION_KEY)
}

function currentUser(): CurrentUser {
  const raw = sessionStorage.getItem(SESSION_KEY)
  if (raw) return JSON.parse(raw) as CurrentUser
  return {
    id: 'u-analyst-01',
    username: 'analyst01',
    displayName: '张若楠',
    role: 'analyst',
    mustChangePassword: false,
  }
}

// ---------------------------------------------------------------- 数据源与工单

export async function listDataSources(): Promise<DataSource[]> {
  await sleep()
  return clone(mockDataSources)
}

export interface CaseQuery {
  dataSourceId?: string
  keyword?: string
  status?: string
}

export async function listExternalCases(q: CaseQuery): Promise<ExternalCase[]> {
  await sleep(350)
  let items = mockCases.slice()
  if (q.dataSourceId) items = items.filter((c) => c.dataSourceId === q.dataSourceId)
  if (q.status && q.status !== 'all') items = items.filter((c) => c.status === q.status)
  if (q.keyword) {
    const k = q.keyword.trim().toLowerCase()
    items = items.filter(
      (c) =>
        c.externalCaseKey.toLowerCase().includes(k) ||
        c.title.toLowerCase().includes(k) ||
        c.customerName.toLowerCase().includes(k),
    )
  }
  return clone(items)
}

export async function getExternalCase(id: string): Promise<ExternalCase> {
  await sleep(300)
  const c = mockCases.find((x) => x.externalCaseId === id)
  if (!c) throw new ApiError(40401, '工单不存在或无权访问')
  return clone(c)
}

// ---------------------------------------------------------------- 任务与模拟 SSE

type Listener = (e: TaskEvent) => void
const listeners = new Map<string, Set<Listener>>()

function emit(taskId: string, type: TaskEvent['type'], title: string, detail?: string) {
  const events = store.events[taskId] ?? (store.events[taskId] = [])
  const ev: TaskEvent = {
    taskId,
    seq: events.length + 1,
    type,
    occurredAt: new Date().toISOString(),
    title,
    detail,
  }
  events.push(ev)
  listeners.get(taskId)?.forEach((cb) => cb(ev))
}

function runSimulator(taskId: string) {
  let i = 0
  const step = () => {
    const task = store.tasks.find((t) => t.taskId === taskId)
    if (!task) return
    // Worker 在步骤边界检查取消
    if (task.status === 'cancel_requested') {
      task.status = 'cancelled'
      task.completedAt = new Date().toISOString()
      emit(taskId, 'task_cancelled', '任务已取消', '已完成的步骤与证据保留，可用于审计')
      return
    }
    if (i >= liveScript.length) return
    const s = liveScript[i]
    i += 1
    setTimeout(() => {
      const t = store.tasks.find((x) => x.taskId === taskId)
      if (!t) return
      if (t.status === 'cancel_requested') {
        step()
        return
      }
      if (s.type === 'task_started') t.status = 'running'
      if (s.type === 'task_succeeded') {
        const extCase = mockCases.find((c) => c.externalCaseId === t.externalCaseId)!
        const reportId = `rpt-${taskId}`
        store.reports[reportId] = buildReport(
          taskId,
          reportId,
          extCase,
          new Date().toISOString(),
        )
        t.reportId = reportId
        t.status = 'succeeded'
        t.completedAt = new Date().toISOString()
      }
      const now = new Date().toISOString()
      if (s.evidence) {
        const list = store.evidence[taskId] ?? (store.evidence[taskId] = [])
        list.push(buildEvidence(taskId, s.evidence, now))
      }
      if (s.tool) {
        const list =
          store.toolExecutions[taskId] ?? (store.toolExecutions[taskId] = [])
        list.push(buildToolExecution(taskId, s.tool, now))
      }
      emit(taskId, s.type, s.title, s.detail)
      step()
    }, s.afterMs)
  }
  step()
}

// 演示剧本：ec-00131 每个会话首次创建时模拟“确认后外部工单发生变化”（40923）。
const fingerprintTriggered = new Set<string>()

export async function createDiagnosisTask(
  input: CreateDiagnosisInput,
): Promise<{ taskId: string }> {
  await sleep(500)
  const extCase = mockCases.find((c) => c.externalCaseId === input.externalCaseId)
  if (!extCase) throw new ApiError(40401, '工单不存在或无权访问')
  if (input.evidenceDataSourceIds.length === 0) {
    throw new ApiError(42201, '至少选择一个证据数据源')
  }
  if (
    input.externalCaseId === 'ec-00131' &&
    !fingerprintTriggered.has(input.externalCaseId)
  ) {
    fingerprintTriggered.add(input.externalCaseId)
    throw new ApiError(40923, '外部工单在确认后发生变化，请刷新工单并重新确认')
  }
  const taskId = `task-${Date.now().toString(36)}`
  const names = mockDataSources
    .filter((d) => input.evidenceDataSourceIds.includes(d.id))
    .map((d) => d.name)
  const task: DiagnosisTask = {
    taskId,
    externalCaseId: extCase.externalCaseId,
    externalCaseKey: extCase.externalCaseKey,
    caseTitle: extCase.title,
    status: 'pending',
    createdBy: currentUser().username,
    createdAt: new Date().toISOString(),
    completedAt: null,
    dataSourceNames: names,
    requestText: input.requestText,
    attachmentNames: input.attachmentNames,
    reportId: null,
    errorMessage: null,
    retryOfTaskId: input.retryOfTaskId ?? null,
  }
  store.tasks.unshift(task)
  emit(taskId, 'task_created', '任务已创建', '已保存工单快照、请求范围与首个事件')
  runSimulator(taskId)
  return { taskId }
}

export interface TaskQuery {
  status?: string
  createdBy?: string
}

export async function listTasks(q: TaskQuery): Promise<DiagnosisTask[]> {
  await sleep(300)
  let items = store.tasks.slice()
  if (q.status && q.status !== 'all') items = items.filter((t) => t.status === q.status)
  if (q.createdBy && q.createdBy !== 'all') {
    items = items.filter((t) => t.createdBy === q.createdBy)
  }
  items.sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  return clone(items)
}

export async function getTask(taskId: string): Promise<DiagnosisTask> {
  await sleep(200)
  const t = store.tasks.find((x) => x.taskId === taskId)
  if (!t) throw new ApiError(40401, '任务不存在或无权访问')
  return clone(t)
}

export async function cancelTask(taskId: string): Promise<void> {
  await sleep(250)
  const t = store.tasks.find((x) => x.taskId === taskId)
  if (!t) throw new ApiError(40401, '任务不存在或无权访问')
  if (t.status === 'cancel_requested' || t.status === 'cancelled') return
  if (t.status !== 'pending' && t.status !== 'running') {
    throw new ApiError(40921, '任务当前状态不允许取消')
  }
  t.status = 'cancel_requested'
  emit(taskId, 'cancel_requested', '收到取消请求', '将在当前步骤边界停止后续执行')
}

/** admin 恢复失败任务：同一任务置回 pending，追加审计事件后重新执行 */
export async function recoverTask(taskId: string, reason: string): Promise<void> {
  await sleep(300)
  const t = store.tasks.find((x) => x.taskId === taskId)
  if (!t) throw new ApiError(40401, '任务不存在或无权访问')
  if (currentUser().role !== 'admin') throw new ApiError(40301, '仅 admin 可恢复任务')
  if (t.status !== 'failed' || t.reportId) {
    throw new ApiError(40921, '任务当前状态不允许恢复')
  }
  t.status = 'pending'
  t.completedAt = null
  t.errorMessage = null
  emit(
    taskId,
    'task_requeued',
    '任务已重新入队',
    `admin 恢复：${reason || '未填写原因'}（已记录审计）`,
  )
  runSimulator(taskId)
}

/**
 * 模拟 SSE：先补读 afterSeq 之后的历史事件，再持续推送新事件。
 * onStatus 模拟连接状态（运行中的任务在第 ~5 秒演示一次断线重连）。
 * 真实实现换成 EventSource + Last-Event-ID，状态来自 onerror/onopen。
 */
export function subscribeTaskEvents(
  taskId: string,
  afterSeq: number,
  onEvent: Listener,
  onStatus?: (s: SseConnectionState) => void,
): () => void {
  let active = true
  const timers: number[] = []
  const replay = (store.events[taskId] ?? []).filter((e) => e.seq > afterSeq)
  queueMicrotask(() => {
    if (!active) return
    replay.forEach((e) => onEvent(clone(e)))
  })
  const listener: Listener = (e) => {
    if (active) onEvent(clone(e))
  }
  let set = listeners.get(taskId)
  if (!set) {
    set = new Set()
    listeners.set(taskId, set)
  }
  set.add(listener)

  if (onStatus) {
    onStatus('connected')
    const task = store.tasks.find((t) => t.taskId === taskId)
    if (task && (task.status === 'pending' || task.status === 'running')) {
      timers.push(
        window.setTimeout(() => {
          if (active) onStatus('reconnecting')
        }, 5000),
        window.setTimeout(() => {
          if (active) onStatus('connected')
        }, 6500),
      )
    }
  }

  return () => {
    active = false
    timers.forEach((t) => window.clearTimeout(t))
    set.delete(listener)
  }
}

export async function getTaskEvidence(taskId: string): Promise<EvidenceItem[]> {
  await sleep(250)
  return clone(store.evidence[taskId] ?? [])
}

export async function getToolExecutions(taskId: string): Promise<ToolExecution[]> {
  await sleep(250)
  const list = clone(store.toolExecutions[taskId] ?? [])
  if (currentUser().role !== 'admin') {
    // analyst 不返回 Token 与成本（api.md 权限规则）
    for (const t of list) {
      delete t.tokens
      delete t.costText
    }
  }
  return list
}

// ---------------------------------------------------------------- 报告与反馈

export async function getReportByTask(taskId: string): Promise<DiagnosisReport> {
  await sleep(300)
  const t = store.tasks.find((x) => x.taskId === taskId)
  if (!t || !t.reportId) throw new ApiError(40401, '报告不存在或任务尚未完成')
  return clone(store.reports[t.reportId])
}

export async function submitReview(
  reportId: string,
  verdict: ReviewVerdict,
  comment: string,
): Promise<ReportReview> {
  await sleep(350)
  const report = store.reports[reportId]
  if (!report) throw new ApiError(40401, '报告不存在或无权访问')
  const review: ReportReview = {
    reviewId: `rv-${Date.now().toString(36)}`,
    verdict,
    comment,
    reviewedBy: currentUser().displayName,
    reviewedAt: new Date().toISOString(),
  }
  report.reviews.push(review)
  return clone(review)
}

// ---------------------------------------------------------------- 知识助手（M2）

type ConversationListener = (messages: ChatMessage[]) => void
const convListeners = new Map<string, Set<ConversationListener>>()
const streamTimers = new Map<string, number>()

function notifyConversation(conversationId: string) {
  const msgs = clone(store.messages[conversationId] ?? [])
  convListeners.get(conversationId)?.forEach((cb) => cb(msgs))
}

export async function listConversations(): Promise<Conversation[]> {
  await sleep(200)
  return clone(
    store.conversations
      .slice()
      .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)),
  )
}

export async function createConversation(): Promise<Conversation> {
  await sleep(150)
  const conv: Conversation = {
    conversationId: `conv-${Date.now().toString(36)}`,
    title: '新对话',
    updatedAt: new Date().toISOString(),
    messageCount: 0,
  }
  store.conversations.unshift(conv)
  store.messages[conv.conversationId] = []
  return clone(conv)
}

export function subscribeConversation(
  conversationId: string,
  cb: ConversationListener,
): () => void {
  let set = convListeners.get(conversationId)
  if (!set) {
    set = new Set()
    convListeners.set(conversationId, set)
  }
  set.add(cb)
  queueMicrotask(() => cb(clone(store.messages[conversationId] ?? [])))
  return () => {
    set.delete(cb)
  }
}

export async function sendAssistantMessage(
  conversationId: string,
  text: string,
  attachments: MessageAttachment[],
  webSearchEnabled: boolean,
): Promise<void> {
  await sleep(150)
  const conv = store.conversations.find((c) => c.conversationId === conversationId)
  if (!conv) throw new ApiError(40401, '会话不存在或无权访问')
  const msgs = store.messages[conversationId] ?? (store.messages[conversationId] = [])

  msgs.push({
    messageId: `msg-${Date.now().toString(36)}-u`,
    conversationId,
    role: 'user',
    content: text,
    status: 'completed',
    createdAt: new Date().toISOString(),
    attachments,
    sources: [],
  })
  if (conv.title === '新对话') {
    conv.title = text.length > 16 ? `${text.slice(0, 16)}…` : text
  }
  conv.updatedAt = new Date().toISOString()
  conv.messageCount = msgs.length
  notifyConversation(conversationId)

  // 助手消息：pending → streaming → completed / interrupted
  const answer = pickAnswer(text, webSearchEnabled)
  const assistant: ChatMessage = {
    messageId: `msg-${Date.now().toString(36)}-a`,
    conversationId,
    role: 'assistant',
    content: '',
    status: 'streaming',
    createdAt: new Date().toISOString(),
    attachments: [],
    sources: [],
  }
  msgs.push(assistant)
  notifyConversation(conversationId)

  let pos = 0
  const timer = window.setInterval(() => {
    pos += 4
    assistant.content = answer.content.slice(0, pos)
    if (pos >= answer.content.length) {
      window.clearInterval(timer)
      streamTimers.delete(conversationId)
      assistant.status = 'completed'
      assistant.sources = answer.sources
      conv.updatedAt = new Date().toISOString()
      conv.messageCount = msgs.length
    }
    notifyConversation(conversationId)
  }, 45)
  streamTimers.set(conversationId, timer)
}

/** 用户停止生成：保留已生成内容并标记 interrupted */
export async function stopGeneration(conversationId: string): Promise<void> {
  const timer = streamTimers.get(conversationId)
  if (timer !== undefined) {
    window.clearInterval(timer)
    streamTimers.delete(conversationId)
  }
  const msgs = store.messages[conversationId] ?? []
  const streaming = msgs.find((m) => m.status === 'streaming')
  if (streaming) {
    streaming.status = 'interrupted'
    notifyConversation(conversationId)
  }
}

// ---------------------------------------------------------------- 知识库（M2）

export async function listKnowledgeDocs(
  scope: 'global' | 'personal',
): Promise<KnowledgeDoc[]> {
  await sleep(300)
  const user = currentUser()
  return clone(
    store.knowledgeDocs.filter(
      (d) =>
        d.scope === scope &&
        (scope === 'global' || d.owner === user.displayName),
    ),
  )
}

const demoUploadNames = [
  '客户培训问答记录.pdf',
  '产线布局照片汇总.pdf',
  '接口对接说明-v2.docx',
]
let uploadCounter = 0

/** 模拟入库：processing → ready（4 秒后），对应 Ingestion Worker 异步解析 */
export async function uploadKnowledgeDoc(
  scope: 'personal' | 'global',
): Promise<KnowledgeDoc> {
  await sleep(450)
  const user = currentUser()
  if (scope === 'global' && user.role !== 'admin') {
    throw new ApiError(40301, '全局知识库仅由管理员维护')
  }
  const title = demoUploadNames[uploadCounter % demoUploadNames.length]
  uploadCounter += 1
  const doc: KnowledgeDoc = {
    documentId: `kd-${Date.now().toString(36)}`,
    title,
    scope,
    fileType: title.endsWith('.docx') ? 'DOCX' : 'PDF',
    sizeBytes: 1_500_000 + uploadCounter * 337_000,
    status: 'processing',
    chunkCount: 0,
    owner: user.displayName,
    updatedAt: new Date().toISOString(),
  }
  store.knowledgeDocs.unshift(doc)
  window.setTimeout(() => {
    const d = store.knowledgeDocs.find((x) => x.documentId === doc.documentId)
    if (d && d.status === 'processing') {
      d.status = 'ready'
      d.chunkCount = 36 + uploadCounter * 7
      d.elementSummary = '文本 28 · 表格 4 · 截图 4'
      d.updatedAt = new Date().toISOString()
    }
  }, 4000)
  return clone(doc)
}

export async function listCaseCards(): Promise<CaseCard[]> {
  await sleep(300)
  return clone(store.caseCards)
}

// ---------------------------------------------------------------- Schema Catalog(admin)

export async function listAdminDataSources(): Promise<AdminDataSource[]> {
  await sleep(300)
  return clone(store.adminDataSources)
}

export async function listCatalogVersions(
  dataSourceId: string,
): Promise<CatalogVersion[]> {
  await sleep(250)
  return clone(
    store.catalogVersions
      .filter((v) => v.dataSourceId === dataSourceId)
      .sort((a, b) => b.version - a.version),
  )
}

export async function listCatalogEntries(versionId: string): Promise<CatalogEntry[]> {
  await sleep(250)
  return clone(store.catalogEntries.filter((e) => e.versionId === versionId))
}

/** 创建低频受控 Schema 扫描：running → succeeded（3.5 秒后生成草稿） */
export async function startCatalogScan(dataSourceId: string): Promise<void> {
  await sleep(300)
  const ds = store.adminDataSources.find((d) => d.id === dataSourceId)
  if (!ds) throw new ApiError(40401, '数据源不存在')
  const hasActive = store.catalogVersions.some(
    (v) => v.dataSourceId === dataSourceId && v.scanStatus === 'running',
  )
  if (hasActive) throw new ApiError(40901, '该数据源已有进行中的扫描')
  const maxVersion = Math.max(
    0,
    ...store.catalogVersions
      .filter((v) => v.dataSourceId === dataSourceId)
      .map((v) => v.version),
  )
  const version: CatalogVersion = {
    versionId: `cv-${Date.now().toString(36)}`,
    dataSourceId,
    version: maxVersion + 1,
    status: 'draft',
    scanStatus: 'running',
    entryCount: 0,
    createdBy: currentUser().username,
    createdAt: new Date().toISOString(),
    publishedAt: null,
  }
  store.catalogVersions.push(version)
  ds.lastScanStatus = 'running'
  window.setTimeout(() => {
    version.scanStatus = 'succeeded'
    version.entryCount = 9
    ds.lastScanStatus = 'succeeded'
    const base = store.catalogEntries.filter((e) => e.versionId === 'cv-4')
    for (const e of base) {
      store.catalogEntries.push({
        ...e,
        entryId: `${version.versionId}-${e.entryId}`,
        versionId: version.versionId,
      })
    }
  }, 3500)
}

export async function updateCatalogEntry(
  entryId: string,
  patch: Partial<Pick<CatalogEntry, 'comment' | 'queryable' | 'sensitivityLevel'>>,
): Promise<void> {
  await sleep(200)
  const e = store.catalogEntries.find((x) => x.entryId === entryId)
  if (!e) throw new ApiError(40401, '条目不存在')
  const v = store.catalogVersions.find((x) => x.versionId === e.versionId)
  if (!v || v.status !== 'draft') {
    throw new ApiError(40901, '只有 draft 版本允许编辑')
  }
  Object.assign(e, patch)
}

export async function publishCatalogVersion(versionId: string): Promise<void> {
  await sleep(350)
  const v = store.catalogVersions.find((x) => x.versionId === versionId)
  if (!v) throw new ApiError(40401, '版本不存在')
  if (v.status !== 'draft' || v.scanStatus !== 'succeeded') {
    throw new ApiError(40901, '只允许发布扫描成功的 draft 版本')
  }
  const current = store.catalogVersions.find(
    (x) => x.dataSourceId === v.dataSourceId && x.status === 'published',
  )
  if (current) current.status = 'retired'
  v.status = 'published'
  v.publishedAt = new Date().toISOString()
  const ds = store.adminDataSources.find((d) => d.id === v.dataSourceId)
  if (ds) ds.publishedCatalogVersion = v.version
}

// ---------------------------------------------------------------- 管理

export async function listUsers(): Promise<AdminUser[]> {
  await sleep(300)
  return clone(store.adminUsers)
}

function requireAdmin() {
  if (currentUser().role !== 'admin') {
    throw new ApiError(40301, '当前用户无权执行该操作')
  }
}

/** 目标用户之外是否还有其他可用 admin（防止失去管理入口） */
function hasOtherActiveAdmin(excludeUserId: string): boolean {
  return store.adminUsers.some(
    (u) => u.id !== excludeUserId && u.role === 'admin' && u.status === 'active',
  )
}

export async function createUser(input: CreateUserInput): Promise<AdminUser> {
  await sleep(400)
  requireAdmin()
  const username = input.username.trim()
  if (!username || !input.displayName.trim()) {
    throw new ApiError(42201, '用户名和姓名不能为空')
  }
  if (input.temporaryPassword.length < 8) {
    throw new ApiError(42201, '临时密码至少 8 位')
  }
  if (store.adminUsers.some((u) => u.username === username)) {
    throw new ApiError(40901, '用户名已存在')
  }
  const user: AdminUser = {
    id: `u-${Date.now().toString(36)}`,
    username,
    displayName: input.displayName.trim(),
    role: input.role,
    status: 'active',
    mustChangePassword: true,
    createdAt: new Date().toISOString(),
    lastLoginAt: null,
  }
  store.adminUsers.push(user)
  return clone(user)
}

export async function setUserStatus(
  userId: string,
  status: 'active' | 'disabled',
): Promise<void> {
  await sleep(300)
  requireAdmin()
  const u = store.adminUsers.find((x) => x.id === userId)
  if (!u) throw new ApiError(40401, '用户不存在')
  if (
    status === 'disabled' &&
    u.role === 'admin' &&
    !hasOtherActiveAdmin(userId)
  ) {
    throw new ApiError(40901, '不能禁用最后一个可用 admin，系统将失去管理入口')
  }
  u.status = status
}

export async function setUserRole(userId: string, role: 'analyst' | 'admin'): Promise<void> {
  await sleep(300)
  requireAdmin()
  const u = store.adminUsers.find((x) => x.id === userId)
  if (!u) throw new ApiError(40401, '用户不存在')
  if (role === 'analyst' && u.role === 'admin' && !hasOtherActiveAdmin(userId)) {
    throw new ApiError(40901, '不能降级最后一个可用 admin，系统将失去管理入口')
  }
  u.role = role
}

export async function resetUserPassword(
  userId: string,
  temporaryPassword: string,
): Promise<void> {
  await sleep(300)
  requireAdmin()
  if (temporaryPassword.length < 8) {
    throw new ApiError(42201, '临时密码至少 8 位')
  }
  const u = store.adminUsers.find((x) => x.id === userId)
  if (!u) throw new ApiError(40401, '用户不存在')
  u.mustChangePassword = true
}

export async function getDependencies(): Promise<DependencyStatus[]> {
  await sleep(400)
  return clone(mockDependencies)
}

export async function getSystemStats(): Promise<SystemStats> {
  await sleep(300)
  const runningTasks = store.tasks.filter(
    (t) => t.status === 'running' || t.status === 'pending',
  ).length
  const failedTasks24h = store.tasks.filter((t) => t.status === 'failed').length
  return { ...clone(mockSystemStats), runningTasks, failedTasks24h }
}

// 死信队列（演示）：messaging.md 的人工恢复流程最小可视化
const deadLetters: DeadLetterMessage[] = [
  {
    messageId: 'msg-01981f2a-7c11',
    queue: 'diagnosis.dead-letter',
    reason: '消息体 schema 版本不受支持（payloadSchemaVersion=0）',
    attempts: 4,
    occurredAt: iso(26 * HOUR),
    taskId: null,
  },
  {
    messageId: 'msg-01981e88-3b06',
    queue: 'diagnosis.dead-letter',
    reason: '任务输入附件已过清理期，Worker 拒绝执行',
    attempts: 3,
    occurredAt: iso(30 * HOUR),
    taskId: 'task-demo-c3',
  },
]

export async function listDeadLetters(): Promise<DeadLetterMessage[]> {
  await sleep(300)
  requireAdmin()
  return clone(deadLetters)
}

export async function requeueDeadLetter(messageId: string): Promise<void> {
  await sleep(350)
  requireAdmin()
  const idx = deadLetters.findIndex((d) => d.messageId === messageId)
  if (idx < 0) throw new ApiError(40401, '死信不存在或已处理')
  deadLetters.splice(idx, 1)
}
