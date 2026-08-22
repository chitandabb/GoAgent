import type { TaskEvent } from '@/shared/api/m1-types'
import { fmtClock } from '@/shared/lib/fmt'

const eventMeta: Record<string, { title: string; kind: 'blue' | 'green' | 'red' | 'orange' | 'gray' }> = {
  task_created: { title: '任务已提交', kind: 'blue' },
  task_cancel_requested: { title: '已请求取消', kind: 'orange' },
  task_started: { title: '开始处理', kind: 'blue' },
  task_reclaimed: { title: '任务已恢复处理', kind: 'orange' },
  task_retry_scheduled: { title: '已安排重试', kind: 'orange' },
  task_succeeded: { title: '排查单已生成', kind: 'green' },
  task_failed: { title: '处理失败', kind: 'red' },
  task_cancelled: { title: '任务已取消', kind: 'gray' },
  task_requeued: { title: '任务已重新提交', kind: 'orange' },
}

function Dot({ kind }: { kind: 'blue' | 'green' | 'red' | 'orange' | 'gray' }) {
  const color = {
    blue: 'bg-primary',
    green: 'bg-ok',
    red: 'bg-danger',
    orange: 'bg-warn',
    gray: 'bg-hairline',
  }[kind]
  return <span className={`relative z-10 mt-1 block size-3 shrink-0 rounded-full ${color}`} />
}

function eventDetail(event: TaskEvent): string | null {
  const preferred = ['message', 'reason', 'errorMessage', 'lastErrorMessage']
  for (const key of preferred) {
    const value = event.payload[key]
    if (typeof value === 'string' && value) return value
  }
  return null
}

export function EventTimeline({ events, live }: { events: TaskEvent[]; live: boolean }) {
  return (
    <ol className="relative">
      <span aria-hidden className="absolute bottom-2 left-[5px] top-2 w-px bg-hairline" />
      {events.map((event) => {
        const meta = eventMeta[event.eventType] ?? { title: event.eventType, kind: 'gray' as const }
        const detail = eventDetail(event)
        return (
          <li key={event.seq} className="flex gap-4 pb-5 last:pb-0">
            <Dot kind={meta.kind} />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline justify-between gap-4">
                <p className="text-[14px] font-semibold text-ink">
                  {meta.title} <span className="ml-1 text-[11px] font-normal text-ink-48">#{event.seq}</span>
                </p>
                <time className="shrink-0 text-[12px] tabular-nums text-ink-48">{fmtClock(event.createdAt)}</time>
              </div>
              {detail && <p className="mt-0.5 break-words text-[12px] leading-[1.5] text-ink-48">{detail}</p>}
            </div>
          </li>
        )
      })}
      {events.length === 0 && !live && <li className="text-[13px] text-ink-48">暂无处理记录</li>}
      {live && (
        <li className="flex gap-4">
          <span className="relative z-10 block size-3 shrink-0">
            <span className="absolute inset-0 animate-ping rounded-full bg-primary/30" />
            <span className="absolute inset-[2px] rounded-full bg-primary" />
          </span>
          <p className="text-[13px] text-ink-48">等待后续进度…</p>
        </li>
      )}
    </ol>
  )
}
