import type { ConversationCitation, ConversationMessage } from '@/shared/api/m1-types'
import { Badge } from '@/shared/ui/Badge'

export function fmtBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export function citationLabel(citation: ConversationCitation): string {
  if (citation.sourceType === 'web') {
    const url = citation.sourceRef.startsWith('http') ? citation.sourceRef : `https://${citation.sourceRef}`
    try {
      const host = new URL(url).hostname.replace(/^www\./, '')
      return host || citation.sourceRef
    } catch {
      return citation.sourceRef
    }
  }
  if (citation.sourceType === 'attachment') {
    return '附件引用'
  }
  if (citation.sourceType === 'knowledge_chunk') {
    return '知识库引用'
  }
  return citation.sourceRef
}

export function citationKindBadge(citation: ConversationCitation): 'orange' | 'blue' | 'gray' {
  if (citation.sourceType === 'web') return 'orange'
  if (citation.sourceType === 'knowledge_chunk') return 'blue'
  return 'gray'
}

function CitationList({
  citations,
  onCitationClick,
}: {
  citations: ConversationCitation[]
  onCitationClick: (citation: ConversationCitation) => void
}) {
  if (citations.length === 0) return null
  return (
    <div className="mt-3 border-t border-divider pt-2.5">
      <p className="mb-1.5 text-[11px] font-semibold text-ink-48">引用</p>
      <ul className="flex flex-col gap-1">
        {citations.map((citation, index) => (
          <li key={`${citation.sourceRef}-${index}`} className="flex items-baseline gap-2 text-[12px]">
            <Badge tone={citationKindBadge(citation)}>
              {citation.sourceType === 'web' ? '网络' : citation.sourceType === 'knowledge_chunk' ? '知识库' : '附件'}
            </Badge>
            {citation.sourceType === 'web' ? (
              <a
                href={citation.sourceRef.startsWith('http') ? citation.sourceRef : `https://${citation.sourceRef}`}
                target="_blank"
                rel="noreferrer"
                className="text-primary hover:underline"
              >
                {citationLabel(citation)}
              </a>
            ) : (
              <button
                type="button"
                onClick={() => onCitationClick(citation)}
                className="press text-primary hover:underline"
              >
                {citationLabel(citation)}
              </button>
            )}
          </li>
        ))}
      </ul>
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
        <div className="max-w-[75%] whitespace-pre-wrap rounded-[18px] rounded-br-[6px] bg-ink px-4 py-2.5 text-[14px] leading-[1.6] text-white">
          {message.content}
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col items-start">
      <div className="max-w-[85%] rounded-[18px] rounded-bl-[6px] border border-hairline bg-canvas px-5 py-4">
        <p className="whitespace-pre-wrap text-[14px] leading-[1.7] text-ink">{message.content}</p>
        <CitationList citations={message.citations} onCitationClick={onCitationClick} />
      </div>
    </div>
  )
}
