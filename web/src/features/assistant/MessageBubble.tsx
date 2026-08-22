import { Link } from 'react-router'
import type {
  ConversationCaseReference,
  ConversationCitation,
  ConversationMessage,
  ConversationTaskReference,
} from '@/shared/api/m1-types'
import { AssistantMarkdown } from './AssistantMarkdown'
import { AnswerActivity } from './AnswerActivity'
import { CitationList } from './CitationList'

const technicalTaskStatusLabels: Record<string, string> = {
  pending: '等待处理',
  running: '处理中',
  succeeded: '已完成',
  failed: '处理失败',
  cancelled: '已取消',
  cancel_requested: '取消中',
}

/** 会话正文只展示业务信息，内部任务编号和英文状态由右侧卷宗承载。 */
export function formatAssistantContent(content: string, hasCreatedTask = false): string {
  // 引用 marker 只用于后端校验和引用卡定位，不属于面向用户的回答正文。
  const normalized = content
    .replace(/\s*\[source:(?:knowledge|attachment|web):[^\]\r\n]+\]/gi, '')
    .replace(/\s*\[supporting marker:\s*\]/gi, '')
    .trim()
  if (/任务\s*ID\s*[:：]/i.test(normalized) || (hasCreatedTask && /异步诊断任务|当前状态/i.test(normalized))) {
    return '排查任务已创建，处理进度和结果会在右侧工单卷宗中实时更新。'
  }
  return normalized
    .replace(/^\s*[-*]\s*任务\s*ID[^\n]*$/gim, '')
    .replace(/^\s*[-*]\s*状态[^\n]*$/gim, '')
    .replace(/\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi, '排查任务')
    .replace(/`(pending|running|succeeded|failed|cancelled|cancel_requested)`/gi, (_, status: string) => technicalTaskStatusLabels[status.toLowerCase()] ?? status)
    .replace(/\n{3,}/g, '\n\n')
    .trim()
}

export function fmtBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
}

function ReferenceList({
  caseReferences,
  taskReferences,
}: {
  caseReferences: ConversationCaseReference[]
  taskReferences: ConversationTaskReference[]
}) {
  if (caseReferences.length === 0 && taskReferences.length === 0) return null
  return (
    <div className="mt-2 flex flex-wrap gap-1.5">
      {caseReferences.map((reference) => (
        <Link
          key={`${reference.kind}-${reference.externalCaseId}`}
          to={`/cases/${reference.externalCaseId}`}
          className="press rounded-capsule bg-parchment px-2.5 py-1 text-[11px] font-semibold text-primary hover:underline"
        >
          {reference.kind === 'selected' ? '当前工单' : '提及工单'}
        </Link>
      ))}
      {taskReferences.map((reference) => (
        <Link
          key={`${reference.kind}-${reference.taskId}`}
          to={`/tasks/${reference.taskId}`}
          className="press rounded-capsule bg-pearl px-2.5 py-1 text-[11px] font-semibold text-primary hover:underline"
        >
          {reference.kind === 'created' ? '排查任务已创建 · 查看进度' : '查看关联任务'}
        </Link>
      ))}
    </div>
  )
}

export function MessageBubble({
  message,
  onCitationClick,
}: {
  message: ConversationMessage
  onCitationClick: (citation: ConversationCitation) => void
}) {
  if (message.role === 'user') {
    return (
      <div className="flex flex-col items-end gap-1.5">
        {message.attachments.length > 0 && (
          <div className="flex flex-wrap justify-end gap-1.5">
            {message.attachments.map((attachment) => (
              <span
                key={attachment.attachmentId}
                className="inline-flex items-center gap-1.5 rounded-capsule bg-pearl px-2.5 py-1 text-[11px] text-ink-80"
              >
                <span className="max-w-[180px] truncate">{attachment.originalName}</span>
                <span className="text-ink-48">{fmtBytes(attachment.sizeBytes)}</span>
              </span>
            ))}
          </div>
        )}
        <ReferenceList caseReferences={message.caseReferences} taskReferences={[]} />
        <div className="max-w-[75%] whitespace-pre-wrap rounded-[18px] rounded-br-[6px] bg-primary px-4 py-2.5 text-[14px] leading-[1.6] text-white">
          {message.content}
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col items-start">
      <div className="max-w-[85%] rounded-[18px] rounded-bl-[6px] border border-hairline bg-canvas px-5 py-4">
        <AssistantMarkdown
          content={formatAssistantContent(
            message.content,
            message.taskReferences.some((reference) => reference.kind === 'created'),
          )}
        />
        <ReferenceList caseReferences={message.caseReferences} taskReferences={message.taskReferences} />
        <AnswerActivity
          conversationId={message.conversationId}
          turnId={message.turnId}
          provenance={message.provenance}
        />
        <CitationList message={message} onCitationClick={onCitationClick} />
      </div>
    </div>
  )
}
