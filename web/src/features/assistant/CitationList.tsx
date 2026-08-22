import { useQuery } from '@tanstack/react-query'
import type { ConversationCitation, ConversationMessage } from '@/shared/api/m1-types'
import { getAttachmentPreview } from '@/shared/api/attachments'
import { getKnowledgeCitation } from '@/shared/api/knowledge'
import { Badge } from '@/shared/ui/Badge'

function knowledgeChunkIdFromRef(sourceRef: string): string | null {
  const parts = sourceRef.replace(/^knowledge:/, '').split('/')
  const chunkId = parts.length === 2 ? parts[1] : ''
  return chunkId || null
}

function attachmentIdFromRef(sourceRef: string): string | null {
  const attachmentId = sourceRef.replace(/^attachment:/, '')
  return attachmentId || null
}

function citationTone(citation: ConversationCitation): 'orange' | 'blue' | 'gray' {
  if (citation.sourceType === 'web') return 'orange'
  if (citation.sourceType === 'knowledge_chunk') return 'blue'
  return 'gray'
}

function citationKind(citation: ConversationCitation): string {
  if (citation.sourceType === 'web') return '网络'
  if (citation.sourceType === 'knowledge_chunk') return '知识库'
  return '附件'
}

function webLabel(sourceRef: string): string {
  const url = sourceRef.startsWith('http') ? sourceRef : `https://${sourceRef}`
  try {
    return new URL(url).hostname.replace(/^www\./, '') || sourceRef
  } catch {
    return sourceRef
  }
}

function sourceTitle(
  citation: ConversationCitation,
  knowledgeTitle: string | undefined,
  attachmentName: string | undefined,
): string {
  if (citation.sourceType === 'web') return webLabel(citation.sourceRef)
  if (citation.sourceType === 'knowledge_chunk') return knowledgeTitle || '知识库片段'
  return attachmentName || '会话附件'
}

function attachmentSnippet(data: Awaited<ReturnType<typeof getAttachmentPreview>> | undefined): string {
  return (data?.elements ?? [])
    .slice(0, 2)
    .map((element) => element.contentText.trim())
    .filter(Boolean)
    .join('\n')
}

function CitationPreviewCard({
  citation,
  message,
  index,
  onCitationClick,
}: {
  citation: ConversationCitation
  message: ConversationMessage
  index: number
  onCitationClick: (citation: ConversationCitation) => void
}) {
  const knowledgeChunkId = citation.sourceType === 'knowledge_chunk'
    ? knowledgeChunkIdFromRef(citation.sourceRef)
    : null
  const attachmentId = citation.sourceType === 'attachment'
    ? attachmentIdFromRef(citation.sourceRef)
    : null

  const knowledge = useQuery({
    queryKey: ['knowledge-citation', knowledgeChunkId],
    queryFn: () => getKnowledgeCitation(knowledgeChunkId!),
    enabled: Boolean(knowledgeChunkId),
    retry: false,
    staleTime: 5 * 60 * 1000,
  })
  const attachment = useQuery({
    queryKey: ['attachment-preview', message.conversationId, attachmentId],
    queryFn: () => getAttachmentPreview(message.conversationId, attachmentId!),
    enabled: Boolean(attachmentId),
    retry: false,
    staleTime: 5 * 60 * 1000,
  })

  const isKnowledge = citation.sourceType === 'knowledge_chunk'
  const data = isKnowledge ? knowledge.data : attachment.data
  const title = sourceTitle(citation, knowledge.data?.title, attachment.data?.originalName)
  const pageNumber = isKnowledge
    ? knowledge.data?.pageNumber
    : attachment.data?.elements[0]?.pageNumber
  const sectionPath = isKnowledge
    ? knowledge.data?.sectionPath
    : attachment.data?.elements[0]?.sectionPath
  const snippet = isKnowledge ? knowledge.data?.contentText.trim() : attachmentSnippet(attachment.data)
  const pending = isKnowledge ? knowledge.isPending : attachment.isPending
  const failed = isKnowledge ? knowledge.isError : attachment.isError

  return (
    <li className="rounded-utility border border-hairline bg-parchment/70 px-3 py-2.5">
      <div className="flex min-w-0 items-start gap-2">
        <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-[10px] font-semibold text-primary">
          {index + 1}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <Badge tone={citationTone(citation)}>{citationKind(citation)}</Badge>
            {citation.sourceType === 'web' ? (
              <a
                href={citation.sourceRef.startsWith('http') ? citation.sourceRef : `https://${citation.sourceRef}`}
                target="_blank"
                rel="noreferrer"
                className="truncate text-[12px] font-semibold text-primary hover:underline"
              >
                {title}
              </a>
            ) : (
              <button
                type="button"
                onClick={() => onCitationClick(citation)}
                className="press min-w-0 truncate text-left text-[12px] font-semibold text-primary hover:underline"
                title="打开完整来源片段"
              >
                {title}
              </button>
            )}
            {pageNumber !== undefined && <span className="text-[11px] text-ink-48">第 {pageNumber} 页</span>}
          </div>
          {sectionPath && sectionPath.length > 0 && (
            <p className="mt-1 truncate text-[10px] text-ink-48">{sectionPath.join(' / ')}</p>
          )}
          {citation.sourceType === 'web' ? (
            <p className="mt-1.5 text-[11px] leading-[1.55] text-ink-48">外部网页来源，打开链接查看原文</p>
          ) : pending ? (
            <p className="mt-1.5 text-[11px] leading-[1.55] text-ink-48">正在读取来源片段…</p>
          ) : failed || !data ? (
            <p className="mt-1.5 text-[11px] leading-[1.55] text-ink-48">暂时无法读取片段，可打开完整来源重试</p>
          ) : (
            <p className="mt-1.5 line-clamp-3 whitespace-pre-wrap text-[12px] leading-[1.65] text-ink-80">
              {snippet || '该来源没有可展示的文本片段'}
            </p>
          )}
          {citation.sourceType !== 'web' && (
            <button
              type="button"
              onClick={() => onCitationClick(citation)}
              className="press mt-1.5 text-[11px] font-semibold text-primary hover:underline"
            >
              查看完整片段
            </button>
          )}
        </div>
      </div>
    </li>
  )
}

export function CitationList({
  message,
  onCitationClick,
}: {
  message: ConversationMessage
  onCitationClick: (citation: ConversationCitation) => void
}) {
  const citations = message.citations
  if (citations.length === 0) return null

  return (
    <section className="mt-4 border-t border-divider pt-3" aria-label="来源">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="text-[11px] font-semibold tracking-[0.08em] text-ink-48">来源</span>
        <span className="text-[10px] tabular-nums text-ink-48">{citations.length} 条</span>
      </div>
      <ol className="flex flex-col gap-2">
        {citations.map((citation, index) => (
          <CitationPreviewCard
            key={`${citation.sourceRef}-${index}`}
            citation={citation}
            message={message}
            index={index}
            onCitationClick={onCitationClick}
          />
        ))}
      </ol>
    </section>
  )
}
