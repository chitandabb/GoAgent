// 诊断执行脚本：模拟 Diagnosis Worker 按步骤推进时产生的 TaskEvent 序列，
// 以及联动生成的 EvidenceItem、ToolExecution 和报告内容。
// 接入真实后端后随 mocks 目录一并删除。
import type {
  DiagnosisReport,
  EvidenceItem,
  ExternalCase,
  TaskEventType,
  ToolExecution,
} from '@/shared/api/types'

export type EvidenceKey = 'snap' | 'sql1' | 'sql2' | 'kb'

export interface ScriptTool {
  stepName: string
  toolName: string
  status: ToolExecution['status']
  durationMs: number
  rowCount?: number
  truncated?: boolean
  tokens?: number
  costText?: string
}

export interface ScriptEvent {
  afterMs: number
  type: TaskEventType
  title: string
  detail?: string
  evidence?: EvidenceKey
  tool?: ScriptTool
}

// task_created 在创建任务的“事务”里立即产生，不在脚本内。
export const liveScript: ScriptEvent[] = [
  {
    afterMs: 900,
    type: 'task_started',
    title: '诊断开始执行',
    detail: 'Diagnosis Worker 已领取任务',
  },
  { afterMs: 600, type: 'step_started', title: '读取工单快照' },
  {
    afterMs: 1000,
    type: 'tool_succeeded',
    title: '快照解析完成',
    detail: '提取工单字段 14 项、附件引用与请求范围',
    evidence: 'snap',
    tool: {
      stepName: '读取工单快照',
      toolName: 'case_snapshot_reader',
      status: 'succeeded',
      durationMs: 950,
    },
  },
  { afterMs: 400, type: 'step_completed', title: '工单快照分析完成' },
  { afterMs: 500, type: 'step_started', title: '受控数据库查询' },
  {
    afterMs: 1500,
    type: 'tool_succeeded',
    title: '受控查询 #1 完成',
    detail: '报工记录查询返回 12 行（字段已脱敏）',
    evidence: 'sql1',
    tool: {
      stepName: '受控数据库查询',
      toolName: 'controlled_sql_query',
      status: 'succeeded',
      durationMs: 1430,
      rowCount: 12,
    },
  },
  {
    afterMs: 1300,
    type: 'tool_succeeded',
    title: '受控查询 #2 完成',
    detail: '库存流水查询返回 8 行（超出部分已截断）',
    evidence: 'sql2',
    tool: {
      stepName: '受控数据库查询',
      toolName: 'controlled_sql_query',
      status: 'succeeded',
      durationMs: 1210,
      rowCount: 8,
      truncated: true,
    },
  },
  { afterMs: 400, type: 'step_completed', title: '数据库证据采集完成' },
  { afterMs: 500, type: 'step_started', title: '知识库检索' },
  {
    afterMs: 1100,
    type: 'tool_succeeded',
    title: '案例检索完成',
    detail: '命中 2 个相似历史案例',
    evidence: 'kb',
    tool: {
      stepName: '知识库检索',
      toolName: 'knowledge_retriever',
      status: 'succeeded',
      durationMs: 1040,
      rowCount: 2,
    },
  },
  { afterMs: 300, type: 'step_completed', title: '知识库检索完成' },
  { afterMs: 600, type: 'step_started', title: '生成诊断报告' },
  {
    afterMs: 1700,
    type: 'report_generated',
    title: '报告已生成',
    detail: '结论、证据引用与风险等级已通过结构化解析校验',
    tool: {
      stepName: '生成诊断报告',
      toolName: 'chat_model.report',
      status: 'succeeded',
      durationMs: 1650,
      tokens: 3840,
      costText: '¥0.12',
    },
  },
  { afterMs: 300, type: 'task_succeeded', title: '诊断完成' },
]

export function buildEvidence(
  taskId: string,
  key: EvidenceKey,
  collectedAt: string,
): EvidenceItem {
  switch (key) {
    case 'snap':
      return {
        evidenceId: `${taskId}-ev-snap-1`,
        sourceType: 'case_snapshot',
        summary: '发起诊断时保存的不可变工单快照，含描述、客户与产品版本信息',
        location: 'CaseSnapshot · 创建于任务发起事务',
        collectedAt,
      }
    case 'sql1':
      return {
        evidenceId: `${taskId}-ev-sql-1`,
        sourceType: 'sql_query',
        summary:
          '报工记录查询：目标工序存在报工数据，但关键关联字段为空（返回 12 行，字段已脱敏）',
        location: 'MES 生产库（演示） · 受控查询 #1',
        collectedAt,
      }
    case 'sql2':
      return {
        evidenceId: `${taskId}-ev-sql-2`,
        sourceType: 'sql_query',
        summary:
          '库存/流水核对查询：异常记录集中在特定时段，与正常记录的写入路径不同（返回 8 行，已截断）',
        location: 'MES 生产库（演示） · 受控查询 #2',
        collectedAt,
      }
    case 'kb':
      return {
        evidenceId: `${taskId}-ev-kb-1`,
        sourceType: 'knowledge_case',
        summary:
          '相似案例 KB-0173：同类现象的已确认根因为关联映射表未回写，处理方式已验证',
        location: '全局知识库 · 案例卡片 KB-0173',
        collectedAt,
      }
  }
}

let toolSeq = 0
export function buildToolExecution(
  taskId: string,
  tool: ScriptTool,
  startedAt: string,
): ToolExecution {
  toolSeq += 1
  return {
    executionId: `${taskId}-tx-${toolSeq}`,
    taskId,
    stepName: tool.stepName,
    toolName: tool.toolName,
    status: tool.status,
    startedAt,
    durationMs: tool.durationMs,
    rowCount: tool.rowCount,
    truncated: tool.truncated,
    tokens: tool.tokens,
    costText: tool.costText,
  }
}

export function buildReport(
  taskId: string,
  reportId: string,
  extCase: ExternalCase,
  generatedAt: string,
): DiagnosisReport {
  const keys: EvidenceKey[] = ['snap', 'sql1', 'sql2', 'kb']
  const ev = keys.map((k) => buildEvidence(taskId, k, generatedAt))

  // ec-00131 演示 inconclusive：证据不足时明确拒绝判断，不强行猜测根因。
  if (extCase.externalCaseId === 'ec-00131') {
    return {
      reportId,
      taskId,
      conclusionStatus: 'inconclusive',
      riskLevel: 'low',
      business: {
        overview: `围绕工单 ${extCase.externalCaseKey}「${extCase.title}」的诊断已正常完成，但当前证据不足以确定根因。系统检查了工序流转记录与报工数据，未发现能够解释“OP20 未完成”提示的直接数据异常；为避免误导，本报告不给出推测性结论。`,
        likelyCause: '证据不足，未确定。',
        impact: '未发现数据损坏或跨工单影响；工单关闭受阻为当前唯一已确认影响。',
        suggestion: '请按“下一步建议”补充信息后重新发起诊断。',
        needDeveloper: false,
      },
      claims: [
        {
          claimId: `${taskId}-c1`,
          statement: 'OP20 的报工记录在数据库中存在且状态为已完成，与界面提示矛盾',
          evidenceIds: [`${taskId}-ev-sql-1`],
        },
        {
          claimId: `${taskId}-c2`,
          statement: '工序完成状态的汇总视图与明细记录存在口径差异，但差异来源未能定位',
          evidenceIds: [`${taskId}-ev-sql-2`],
        },
      ],
      evidence: ev.filter((e) => e.sourceType !== 'knowledge_case'),
      limitations: [
        '知识库中没有与该现象匹配的已确认案例',
        '结论基于演示数据集，实际环境需按下一步建议补充证据',
      ],
      inconclusiveDetail: {
        checked: [
          '工单快照与工艺路线定义',
          'OP20 报工明细记录（存在且状态为已完成）',
          '工序完成状态汇总视图与明细的口径比对',
        ],
        missing: [
          '工单关闭校验的应用端日志（无法从数据库侧获取）',
          '汇总视图的刷新作业执行记录',
          '现场操作时间线（是否发生过工序回退或补录）',
        ],
        nextSteps: [
          '请管理员导出关闭校验的应用日志后，补充为附件重新发起诊断',
          '确认汇总作业最近一次执行时间，若滞后可先手动刷新后重试关闭',
          '如现象仍在，建议开发人员对照 c2 的口径差异检查关闭校验 SQL',
        ],
      },
      generatedAt,
      modelVersion: 'step-2-demo · prompt v1',
      reviews: [],
    }
  }

  return {
    reportId,
    taskId,
    conclusionStatus: 'probable',
    riskLevel: 'medium',
    business: {
      overview: `围绕工单 ${extCase.externalCaseKey}「${extCase.title}」的诊断已完成。系统读取了工单快照、执行了受控数据库查询，并检索了历史案例。主要发现：相关业务记录存在，但关键关联字段异常，导致下游流程未按预期触发。`,
      likelyCause:
        '最可能的原因是业务记录写入时关联字段未正确回写，使后续流程无法定位到对应记录。该现象与历史案例 KB-0173 的已确认根因高度相似。',
      impact:
        '影响范围集中在该工单关联的业务链路，未发现跨工单的数据异常。异常记录仍保留在数据库中，不存在数据丢失。',
      suggestion:
        '建议先按历史案例 KB-0173 的已验证处理方式，由管理员在系统维护界面补齐关联字段（L2）；若补齐后仍未恢复，需要开发人员核查写入逻辑（L3）。',
      needDeveloper: true,
    },
    claims: [
      {
        claimId: `${taskId}-c1`,
        statement: '目标业务记录存在，但关键关联字段为空，导致链路中断',
        evidenceIds: [`${taskId}-ev-sql-1`],
      },
      {
        claimId: `${taskId}-c2`,
        statement: '异常记录时间分布集中，写入路径与正常记录不同，疑与特定操作场景相关',
        evidenceIds: [`${taskId}-ev-sql-2`, `${taskId}-ev-snap-1`],
      },
      {
        claimId: `${taskId}-c3`,
        statement: '历史案例库中存在现象一致、根因已人工确认的案例，处理方式可参考',
        evidenceIds: [`${taskId}-ev-kb-1`],
      },
    ],
    evidence: ev,
    limitations: [
      '未能访问现场应用服务器日志，异常写入的具体触发操作无法完全确认',
      '结论基于演示数据集，实际环境需按报告中的查询范围复核',
    ],
    generatedAt,
    modelVersion: 'step-2-demo · prompt v1',
    reviews: [],
  }
}

// 把脚本立即物化成一段已完成的事件历史（用于预置演示任务）。
export function materializeEvents(
  startAtMs: number,
  script: ScriptEvent[],
): {
  seq: number
  type: TaskEventType
  title: string
  detail?: string
  occurredAt: string
  evidence?: EvidenceKey
  tool?: ScriptTool
}[] {
  const events: ReturnType<typeof materializeEvents> = [
    {
      seq: 1,
      type: 'task_created',
      title: '任务已创建',
      detail: '已保存工单快照、请求范围与首个事件',
      occurredAt: new Date(startAtMs).toISOString(),
    },
  ]
  let t = startAtMs
  let seq = 1
  for (const s of script) {
    t += s.afterMs
    seq += 1
    events.push({
      seq,
      type: s.type,
      title: s.title,
      detail: s.detail,
      occurredAt: new Date(t).toISOString(),
      evidence: s.evidence,
      tool: s.tool,
    })
  }
  return events
}
