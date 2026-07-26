// 知识助手的演示会话与回答模板。接入真实后端后随 mocks 目录一并删除。
import type { ChatMessage, Conversation, MessageSource } from '@/shared/api/types'
import { iso } from './data'

const MIN = 60_000
const HOUR = 3_600_000

export const seedConversations: Conversation[] = [
  {
    conversationId: 'conv-demo-1',
    title: '报工与库存联动机制',
    updatedAt: iso(2 * HOUR),
    messageCount: 2,
  },
  {
    conversationId: 'conv-demo-2',
    title: '追溯断链的常见原因',
    updatedAt: iso(26 * HOUR),
    messageCount: 2,
  },
]

export const seedMessages: Record<string, ChatMessage[]> = {
  'conv-demo-1': [
    {
      messageId: 'msg-d1-1',
      conversationId: 'conv-demo-1',
      role: 'user',
      content: '报工完成后库存是怎么联动更新的？哪些配置会影响这个流程？',
      status: 'completed',
      createdAt: iso(2 * HOUR + 3 * MIN),
      attachments: [],
      sources: [],
    },
    {
      messageId: 'msg-d1-2',
      conversationId: 'conv-demo-1',
      role: 'assistant',
      content:
        '在 MES-Pro 中，报工与库存的联动分三步：报工记录写入后，系统根据工序属性判断是否为“入库工序”；若是，则生成一条库存事务（inventory transaction），再由库存服务汇总更新现存量。\n\n影响该流程的关键配置有三处：① 工序定义中的“完工入库”开关——只有末道工序或显式开启的工序才会触发入库；② 报工与库存联动模式（实时/批量），批量模式下入库会延迟到汇总作业执行；③ 仓库与产线的映射关系缺失时，库存事务会进入待处理队列而不是直接入账。\n\n排查“报工成功但库存未更新”时，建议按顺序检查：报工记录是否存在 → 库存事务是否生成 → 事务是否处于待处理状态。',
      status: 'completed',
      createdAt: iso(2 * HOUR + 2 * MIN),
      attachments: [],
      sources: [
        {
          kind: 'knowledge',
          title: '报工与库存联动配置说明',
          location: '全局知识库 · 第 4–6 页',
        },
        {
          kind: 'knowledge',
          title: 'MES-Pro 实施手册 v5.2',
          location: '全局知识库 · 第 118 页 · 表格',
        },
      ],
    },
  ],
  'conv-demo-2': [
    {
      messageId: 'msg-d2-1',
      conversationId: 'conv-demo-2',
      role: 'user',
      content: '批次追溯查询出现断链，一般有哪些原因？',
      status: 'completed',
      createdAt: iso(26 * HOUR + 5 * MIN),
      attachments: [],
      sources: [],
    },
    {
      messageId: 'msg-d2-2',
      conversationId: 'conv-demo-2',
      role: 'assistant',
      content:
        '追溯断链最常见的三类原因：\n\n1. 批次关联字段为空——报工时批次号未正确带出（典型场景是条码补打后旧批次号失效），记录存在但无法被追溯链路串起来；\n2. 工序跳做或补录——实际生产顺序与工艺路线不一致，补录时……',
      status: 'interrupted',
      createdAt: iso(26 * HOUR + 4 * MIN),
      attachments: [],
      sources: [
        {
          kind: 'knowledge',
          title: '案例卡片 KB-0173：条码补打后批次追溯断链',
          location: '全局知识库 · 案例卡片',
        },
      ],
    },
  ],
}

interface AnswerTemplate {
  keywords: string[]
  content: string
  sources: MessageSource[]
}

const templates: AnswerTemplate[] = [
  {
    keywords: ['报工', '库存'],
    content:
      '根据知识库检索结果，报工后库存未更新通常与“完工入库”触发条件有关：只有末道工序或显式开启入库开关的工序才会生成库存事务。\n\n建议先确认三点：① 该工序在工艺路线中是否被标记为入库工序；② 报工记录对应的库存事务是否已生成但停留在待处理队列；③ 联动模式是否为批量（批量模式下入库依赖汇总作业）。\n\n如果需要针对具体工单定位，请在“外部工单”中对该工单发起诊断，系统会以只读方式核对报工与库存流水。',
    sources: [
      {
        kind: 'knowledge',
        title: '报工与库存联动配置说明',
        location: '全局知识库 · 第 4–6 页',
      },
      {
        kind: 'knowledge',
        title: '案例卡片 KB-0173：条码补打后批次追溯断链',
        location: '全局知识库 · 案例卡片',
      },
    ],
  },
  {
    keywords: ['追溯', '批次'],
    content:
      '批次追溯断链的排查建议从数据关联字段入手：先确认断链工序的报工记录是否存在；若记录存在但批次字段为空，大概率是条码补打或批次映射未回写导致（参考案例 KB-0173）。\n\n若报工记录本身缺失，则需要区分工序跳做与数据采集失败两种情况：前者查工艺路线执行日志，后者查采集网关的重传队列。\n\n以上判断均可通过工单诊断自动完成，并生成带证据引用的报告。',
    sources: [
      {
        kind: 'knowledge',
        title: '案例卡片 KB-0173：条码补打后批次追溯断链',
        location: '全局知识库 · 案例卡片',
      },
      {
        kind: 'knowledge',
        title: 'MES-Pro 实施手册 v5.2',
        location: '全局知识库 · 第 203 页',
      },
    ],
  },
]

const fallback: AnswerTemplate = {
  keywords: [],
  content:
    '基于当前知识库，我找到以下相关信息（演示回答）：该问题涉及的功能在实施手册中有对应章节，建议结合具体产品版本确认配置项差异。\n\n如果内部知识不足，可以开启联网检索获取公开资料；涉及具体工单的数据问题，请改用“工单诊断”以获得带证据链的结论。',
  sources: [
    {
      kind: 'knowledge',
      title: 'MES-Pro 实施手册 v5.2',
      location: '全局知识库 · 目录检索',
    },
  ],
}

export function pickAnswer(
  question: string,
  webSearch: boolean,
): { content: string; sources: MessageSource[] } {
  const hit =
    templates.find((t) => t.keywords.some((k) => question.includes(k))) ?? fallback
  const sources = [...hit.sources]
  if (webSearch) {
    sources.push({
      kind: 'web',
      title: 'SQL Server 事务与锁等待排查指南',
      location: '公开网络（已脱敏检索）',
      url: 'https://learn.microsoft.com/sql/relational-databases/performance/',
      retrievedAt: new Date().toISOString(),
    })
  }
  return { content: hit.content, sources }
}
