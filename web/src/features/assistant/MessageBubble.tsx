import type { ChatMessage } from '@/shared/api'
import { generationMeta } from '@/shared/lib/status'
import { Badge } from '@/shared/ui/Badge'

function Sources({ message }: { message: ChatMessage }) {
  if (message.sources.length === 0) return null
  return (
    <div className="mt-3 border-t border-divider pt-2.5">
      <p className="mb-1.5 text-[11px] font-semibold text-ink-48">来源</p>
      <ul className="flex flex-col gap-1">
        {message.sources.map((s, i) => (
          <li key={i} className="flex items-baseline gap-2 text-[12px]">
            <Badge tone={s.kind === 'web' ? 'orange' : 'blue'}>
              {s.kind === 'web' ? '网络' : '知识库'}
            </Badge>
            {s.url ? (
              <a
                href={s.url}
                target="_blank"
                rel="noreferrer"
                className="text-primary hover:underline"
              >
                {s.title}
              </a>
            ) : (
              <span className="text-ink-80">{s.title}</span>
            )}
            <span className="text-ink-48">{s.location}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function MessageBubble({ message }: { message: ChatMessage }) {
  if (message.role === 'user') {
    return (
      <div className="flex flex-col items-end gap-1.5">
        {message.attachments.length > 0 && (
          <div className="flex flex-wrap justify-end gap-1.5">
            {message.attachments.map((a, i) => (
              <span
                key={`${a.name}-${i}`}
                className="rounded-capsule bg-pearl px-2.5 py-1 text-[11px] text-ink-48"
              >
                {a.name} · {a.scope === 'personal' ? '个人库' : '仅本次'}
              </span>
            ))}
          </div>
        )}
        <div className="max-w-[75%] rounded-[18px] rounded-br-[6px] bg-ink px-4 py-2.5 text-[14px] leading-[1.6] text-white">
          {message.content}
        </div>
      </div>
    )
  }

  const meta = generationMeta[message.status]
  return (
    <div className="flex flex-col items-start">
      <div className="max-w-[85%] rounded-[18px] rounded-bl-[6px] border border-hairline bg-canvas px-5 py-4">
        <p className="whitespace-pre-wrap text-[14px] leading-[1.7] text-ink">
          {message.content}
          {message.status === 'streaming' && (
            <span className="ml-0.5 inline-block h-4 w-0.5 animate-pulse bg-primary align-middle" />
          )}
        </p>
        {message.status === 'interrupted' && (
          <div className="mt-2.5">
            <Badge tone={meta.tone}>{meta.label} · 已保留部分内容</Badge>
          </div>
        )}
        {message.status === 'failed' && (
          <div className="mt-2.5">
            <Badge tone="red">生成失败</Badge>
          </div>
        )}
        <Sources message={message} />
      </div>
    </div>
  )
}
