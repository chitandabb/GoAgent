// 演示静态数据。全部为虚构内容，接入真实后端后随 mocks 目录一并删除。
import type {
  AdminUser,
  CurrentUser,
  DataSource,
  DependencyStatus,
  ExternalCase,
  SystemStats,
} from '@/shared/api/types'

const now = Date.now()
const MIN = 60_000
const HOUR = 3_600_000
const DAY = 24 * HOUR
export const iso = (msAgo: number) => new Date(now - msAgo).toISOString()

export const mockAccounts: Array<CurrentUser & { hint: string }> = [
  {
    id: 'u-analyst-01',
    username: 'analyst01',
    displayName: '张若楠',
    role: 'analyst',
    mustChangePassword: false,
    hint: '售后分析员',
  },
  {
    id: 'u-admin-01',
    username: 'admin01',
    displayName: '系统管理员',
    role: 'admin',
    mustChangePassword: false,
    hint: '管理员',
  },
  {
    id: 'u-analyst-02',
    username: 'analyst02',
    displayName: '李文博',
    role: 'analyst',
    mustChangePassword: true,
    hint: '临时密码账号，首次登录须改密',
  },
]

export const mockDataSources: DataSource[] = [
  {
    id: 'ds-mes-demo',
    name: 'MES 生产库（演示）',
    type: 'sqlserver',
    status: 'active',
    environment: 'demo',
  },
  {
    id: 'ds-ref-demo',
    name: '产品参考库（演示）',
    type: 'sqlserver',
    status: 'active',
    environment: 'demo',
  },
]

export const mockCases: ExternalCase[] = [
  {
    externalCaseId: 'ec-00128',
    dataSourceId: 'ds-mes-demo',
    externalCaseKey: 'WO-2026-00128',
    title: '报工后库存未更新',
    description:
      '车间在工序 OP50 完成报工后，成品库存数量没有增加。操作员确认报工界面提示成功，但在库存查询界面看不到对应入库记录。当天同产线其他工单入库正常，问题只出现在该工单的最后一道工序。',
    status: 'open',
    priority: 'high',
    customerName: '苏州精密制造',
    productName: 'MES-Pro',
    productVersion: 'v5.2.1',
    reportedAt: iso(6 * HOUR),
    sourceUpdatedAt: iso(2 * HOUR),
    sourceFingerprint: 'sha256:a1b2c3d4e5f60128',
  },
  {
    externalCaseId: 'ec-00131',
    dataSourceId: 'ds-mes-demo',
    externalCaseKey: 'WO-2026-00131',
    title: '工单无法关闭，提示存在未完成工序',
    description:
      '生产已全部完成并质检合格，但关闭工单时系统提示“存在未完成工序 OP20”。现场核对工序流转卡，OP20 实际已于两天前完成并有纸质签字记录。',
    status: 'open',
    priority: 'medium',
    customerName: '无锡智联装备',
    productName: 'MES-Pro',
    productVersion: 'v5.1.8',
    reportedAt: iso(9 * HOUR),
    sourceUpdatedAt: iso(3 * HOUR),
    sourceFingerprint: 'sha256:b2c3d4e5f6a70131',
  },
  {
    externalCaseId: 'ec-00119',
    dataSourceId: 'ds-mes-demo',
    externalCaseKey: 'WO-2026-00119',
    title: '条码扫描重复报工',
    description:
      '同一序列号条码在报工站被扫描后生成了两条报工记录，导致工单完成数量翻倍。操作员反馈扫描时界面卡顿了几秒，随后连续出现两次成功提示。',
    status: 'processing',
    priority: 'high',
    customerName: '苏州精密制造',
    productName: 'MES-Pro',
    productVersion: 'v5.2.1',
    reportedAt: iso(1 * DAY + 4 * HOUR),
    sourceUpdatedAt: iso(5 * HOUR),
    sourceFingerprint: 'sha256:c3d4e5f6a7b80119',
  },
  {
    externalCaseId: 'ec-00107',
    dataSourceId: 'ds-mes-demo',
    externalCaseKey: 'WO-2026-00107',
    title: '排产计划界面加载超时',
    description:
      '每天早上 8 点左右打开排产计划界面需要等待超过一分钟，偶尔直接超时报错。其他时段响应正常。客户反馈该现象从上周升级后开始出现。',
    status: 'processing',
    priority: 'medium',
    customerName: '南通宏远机械',
    productName: 'MES-Lite',
    productVersion: 'v3.4.0',
    reportedAt: iso(2 * DAY),
    sourceUpdatedAt: iso(1 * DAY),
    sourceFingerprint: 'sha256:d4e5f6a7b8c90107',
  },
  {
    externalCaseId: 'ec-00098',
    dataSourceId: 'ds-mes-demo',
    externalCaseKey: 'WO-2026-00098',
    title: '物料批次追溯记录缺失',
    description:
      '质量部门在做批次追溯时发现，产品批次 B20260721-03 的追溯链路在 OP30、OP40 两道工序处中断，查不到对应的报工与物料消耗记录。生产报表中这两道工序的数量统计是正常的。',
    status: 'closed',
    priority: 'high',
    customerName: '常州华顺电子',
    productName: 'MES-Pro',
    productVersion: 'v5.0.6',
    reportedAt: iso(4 * DAY),
    sourceUpdatedAt: iso(2 * DAY),
    sourceFingerprint: 'sha256:e5f6a7b8c9d00098',
  },
  {
    externalCaseId: 'ec-00092',
    dataSourceId: 'ds-mes-demo',
    externalCaseKey: 'WO-2026-00092',
    title: '交接班报表数量对不上',
    description:
      '夜班交接班报表中的完成数量与白班统计口径不一致，差异固定为 12 件。客户怀疑是跨天时间切分逻辑的问题，希望确认统计口径。',
    status: 'closed',
    priority: 'low',
    customerName: '南通宏远机械',
    productName: 'MES-Lite',
    productVersion: 'v3.4.0',
    reportedAt: iso(6 * DAY),
    sourceUpdatedAt: iso(5 * DAY),
    sourceFingerprint: 'sha256:f6a7b8c9d0e10092',
  },
]

export const mockAdminUsers: AdminUser[] = [
  {
    id: 'u-admin-01',
    username: 'admin01',
    displayName: '系统管理员',
    role: 'admin',
    status: 'active',
    mustChangePassword: false,
    createdAt: iso(30 * DAY),
    lastLoginAt: iso(1 * HOUR),
  },
  {
    id: 'u-analyst-01',
    username: 'analyst01',
    displayName: '张若楠',
    role: 'analyst',
    status: 'active',
    mustChangePassword: false,
    createdAt: iso(28 * DAY),
    lastLoginAt: iso(10 * MIN),
  },
  {
    id: 'u-analyst-02',
    username: 'analyst02',
    displayName: '李文博',
    role: 'analyst',
    status: 'active',
    mustChangePassword: true,
    createdAt: iso(21 * DAY),
    lastLoginAt: iso(2 * DAY),
  },
  {
    id: 'u-analyst-03',
    username: 'analyst03',
    displayName: '陈思远',
    role: 'analyst',
    status: 'disabled',
    mustChangePassword: false,
    createdAt: iso(20 * DAY),
    lastLoginAt: iso(12 * DAY),
  },
]

export const mockDependencies: DependencyStatus[] = [
  {
    name: 'PostgreSQL',
    kind: '事实存储',
    status: 'up',
    latencyMs: 3,
    checkedAt: iso(20_000),
  },
  {
    name: 'Redis',
    kind: '缓存 / SSE 通知',
    status: 'degraded',
    latencyMs: 412,
    checkedAt: iso(20_000),
    message: '响应缓慢，SSE 已退化为 PostgreSQL 轮询',
  },
  {
    name: 'RabbitMQ',
    kind: '任务队列',
    status: 'up',
    latencyMs: 8,
    checkedAt: iso(20_000),
  },
  {
    name: 'MinIO',
    kind: '附件对象存储',
    status: 'up',
    latencyMs: 12,
    checkedAt: iso(20_000),
  },
  {
    name: '外部 SQL Server',
    kind: 'MES 数据源（只读）',
    status: 'up',
    latencyMs: 46,
    checkedAt: iso(20_000),
  },
  {
    name: '模型服务',
    kind: 'Chat / VLM',
    status: 'up',
    latencyMs: 890,
    checkedAt: iso(20_000),
  },
]

export const mockSystemStats: SystemStats = {
  queueBacklog: 0,
  outboxUnpublished: 0,
  failedTasks24h: 1,
  runningTasks: 0,
}
