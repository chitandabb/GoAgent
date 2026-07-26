// 知识库与 Schema Catalog 的演示数据。接入真实后端后随 mocks 目录一并删除。
import type {
  AdminDataSource,
  CaseCard,
  CatalogEntry,
  CatalogVersion,
  KnowledgeDoc,
} from '@/shared/api/types'
import { iso } from './data'

const MIN = 60_000
const HOUR = 3_600_000
const DAY = 24 * HOUR

export const seedKnowledgeDocs: KnowledgeDoc[] = [
  {
    documentId: 'kd-g-01',
    title: 'MES-Pro 实施手册 v5.2',
    scope: 'global',
    fileType: 'PDF',
    sizeBytes: 18_643_212,
    status: 'ready',
    chunkCount: 486,
    elementSummary: '文本 402 · 表格 38 · 截图 31 · 示意图 15',
    owner: '系统管理员',
    updatedAt: iso(12 * DAY),
  },
  {
    documentId: 'kd-g-02',
    title: '报工与库存联动配置说明',
    scope: 'global',
    fileType: 'PDF',
    sizeBytes: 2_314_009,
    status: 'ready',
    chunkCount: 74,
    elementSummary: '文本 58 · 表格 9 · 截图 7',
    owner: '系统管理员',
    updatedAt: iso(8 * DAY),
  },
  {
    documentId: 'kd-g-03',
    title: '常见报错代码对照表',
    scope: 'global',
    fileType: 'XLSX',
    sizeBytes: 412_880,
    status: 'ready',
    chunkCount: 129,
    elementSummary: '表格 129',
    owner: '系统管理员',
    updatedAt: iso(20 * DAY),
  },
  {
    documentId: 'kd-g-04',
    title: '产线数据采集网关部署图册',
    scope: 'global',
    fileType: 'PDF',
    sizeBytes: 9_882_141,
    status: 'processing',
    chunkCount: 0,
    owner: '系统管理员',
    updatedAt: iso(15 * MIN),
  },
  {
    documentId: 'kd-p-01',
    title: '苏州精密项目交接备忘.pdf',
    scope: 'personal',
    fileType: 'PDF',
    sizeBytes: 1_204_552,
    status: 'ready',
    chunkCount: 42,
    elementSummary: '文本 35 · 截图 7',
    owner: '张若楠',
    updatedAt: iso(3 * DAY),
  },
  {
    documentId: 'kd-p-02',
    title: '现场巡检记录-0718.docx',
    scope: 'personal',
    fileType: 'DOCX',
    sizeBytes: 688_010,
    status: 'failed',
    chunkCount: 0,
    owner: '张若楠',
    updatedAt: iso(2 * DAY),
    failReason: 'VLM 图片描述调用失败，已重试 2 次；文档保持不可检索状态',
  },
]

export const seedCaseCards: CaseCard[] = [
  {
    cardId: 'KB-0173',
    title: '条码补打后批次追溯断链',
    productName: 'MES-Pro',
    productVersion: 'v5.0 – v5.2',
    environment: 'SQL Server 2019 · 双数据采集网关',
    symptom:
      '批次追溯链路在个别工序处中断；报工数量统计正常，但追溯查询缺少对应工序记录。',
    rootCause:
      '条码补打后旧批次号未同步，报工写入时批次关联字段为空，批次映射表未回写。',
    solution:
      '管理员在系统维护界面执行“批次关联补齐”；并升级补打程序至 5.2.3 以上版本。',
    verification: '补齐后重新执行追溯查询，断链工序恢复；连续三个批次复查无新增断链。',
    notApplicable: '不适用于手工录入报工的站点；该场景批次字段由人工填写。',
    updatedAt: iso(30 * DAY),
  },
  {
    cardId: 'KB-0158',
    title: '扫描枪重复触发导致重复报工',
    productName: 'MES-Pro',
    productVersion: 'v5.1 及以上',
    environment: '无线扫描枪 · 弱网车间',
    symptom: '同一序列号产生两条报工记录，完成数量翻倍；操作端曾出现卡顿后连续两次成功提示。',
    rootCause:
      '弱网环境下客户端重发请求，服务端未启用报工幂等键，重复请求被当作两次独立报工。',
    solution: '启用报工幂等配置项 idempotent_report=on；清理重复记录使用标准修复脚本 FX-2201。',
    verification: '压测工具模拟重发 100 次，仅产生一条报工记录。',
    notApplicable: '不适用于批量导入报工；导入通道有独立去重逻辑。',
    updatedAt: iso(45 * DAY),
  },
  {
    cardId: 'KB-0141',
    title: '排产界面早高峰加载超时',
    productName: 'MES-Lite',
    productVersion: 'v3.3 – v3.4',
    environment: '单机部署 · 计划报表并存',
    symptom: '每日固定时段排产界面加载超过 60 秒，其余时段正常。',
    rootCause: '夜间统计作业未在早高峰前完成，报表大查询与排产查询争抢同一数据库资源。',
    solution: '调整统计作业窗口至 05:00 前完成；为排产查询增加覆盖索引（见附录脚本）。',
    verification: '连续一周监控早高峰排产查询 P95 < 3 秒。',
    notApplicable: '数据库已做读写分离的站点应先检查复制延迟，而不是直接套用本卡片。',
    updatedAt: iso(60 * DAY),
  },
]

export const seedAdminDataSources: AdminDataSource[] = [
  {
    id: 'ds-mes-demo',
    name: 'MES 生产库（演示）',
    type: 'sqlserver',
    status: 'active',
    environment: 'demo',
    lastCheckStatus: 'up',
    lastCheckAt: iso(3 * MIN),
    publishedCatalogVersion: 3,
    lastScanStatus: 'succeeded',
  },
  {
    id: 'ds-ref-demo',
    name: '产品参考库（演示）',
    type: 'sqlserver',
    status: 'active',
    environment: 'demo',
    lastCheckStatus: 'up',
    lastCheckAt: iso(3 * MIN),
    publishedCatalogVersion: null,
    lastScanStatus: null,
  },
]

export const seedCatalogVersions: CatalogVersion[] = [
  {
    versionId: 'cv-3',
    dataSourceId: 'ds-mes-demo',
    version: 3,
    status: 'published',
    scanStatus: 'succeeded',
    entryCount: 8,
    createdBy: 'admin01',
    createdAt: iso(10 * DAY),
    publishedAt: iso(9 * DAY),
  },
  {
    versionId: 'cv-4',
    dataSourceId: 'ds-mes-demo',
    version: 4,
    status: 'draft',
    scanStatus: 'succeeded',
    entryCount: 9,
    createdBy: 'admin01',
    createdAt: iso(1 * DAY),
    publishedAt: null,
  },
]

function entry(
  versionId: string,
  n: number,
  objectName: string,
  columnName: string,
  dataType: string,
  comment: string,
  aliases: string[],
  queryable: boolean,
  sensitivityLevel: CatalogEntry['sensitivityLevel'],
): CatalogEntry {
  return {
    entryId: `${versionId}-e${n}`,
    versionId,
    schemaName: 'dbo',
    objectName,
    columnName,
    dataType,
    comment,
    semanticAliases: aliases,
    queryable,
    sensitivityLevel,
  }
}

function baseEntries(versionId: string): CatalogEntry[] {
  return [
    entry(versionId, 1, 'work_order', 'wo_no', 'varchar(32)', '生产工单号', ['工单号', '订单编号'], true, 'internal'),
    entry(versionId, 2, 'work_order', 'status', 'tinyint', '工单状态', ['状态'], true, 'internal'),
    entry(versionId, 3, 'report_record', 'lot_id', 'varchar(40)', '批次标识', ['批次号'], true, 'internal'),
    entry(versionId, 4, 'report_record', 'op_code', 'varchar(16)', '工序编码', ['工序'], true, 'internal'),
    entry(versionId, 5, 'report_record', 'report_qty', 'decimal(18,4)', '报工数量', ['数量'], true, 'internal'),
    entry(versionId, 6, 'inventory_txn', 'txn_type', 'varchar(8)', '库存事务类型', ['出入库类型'], true, 'internal'),
    entry(versionId, 7, 'inventory_txn', 'qty', 'decimal(18,4)', '事务数量', [], true, 'internal'),
    entry(versionId, 8, 'operator', 'phone', 'varchar(20)', '操作员电话', [], false, 'sensitive'),
  ]
}

export const seedCatalogEntries: CatalogEntry[] = [
  ...baseEntries('cv-3'),
  ...baseEntries('cv-4'),
  entry(
    'cv-4',
    9,
    'report_record',
    'device_id',
    'varchar(32)',
    '',
    [],
    false,
    'internal',
  ),
]
