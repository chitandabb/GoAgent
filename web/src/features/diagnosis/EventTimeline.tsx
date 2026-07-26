import type { TaskEvent, TaskEventType } from '@/shared/api'
import { fmtClock } from '@/shared/lib/fmt'

type DotKind = 'blue' | 'lifecycle' | 'green' | 'red' | 'orange' | 'gray' | 'tool'

// 层级：生命周期事件（创建/领取）= 实心小蓝点；步骤开始 = 空心蓝环；
// 工具调用 = 灰点；完成/失败/取消 = 语义色。
const kindOf: Record<TaskEventType, DotKind> = {
  task_created: 'lifecycle',
  task_started: 'lifecycle',
  step_started: 'blue',
  tool_succeeded: 'tool',
  step_completed: 'green',
  report_generated: 'green',
  task_succeeded: 'green',
  task_failed: 'red',
  cancel_requested: 'orange',
  task_cancelled: 'gray',
  task_requeued: 'orange',
}

function Dot({ kind }: { kind: DotKind }) {
  const base = 'relative z-10 mt-1 block shrink-0 rounded-full'
  switch (kind) {
    case 'green':
      return (
        <span className={`${base} flex size-4 items-center justify-center bg-ok text-white`}>
          <svg viewBox="0 0 10 10" className="size-2" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="m1.5 5.5 2.5 2.5 4.5-5" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </span>
      )
    case 'red':
      return (
        <span className={`${base} flex size-4 items-center justify-center bg-danger text-white`}>
          <svg viewBox="0 0 10 10" className="size-2" stroke="currentColor" strokeWidth="2">
            <path d="m2 2 6 6M8 2 2 8" strokeLinecap="round" />
          </svg>
        </span>
      )
    case 'orange':
      return <span className={`${base} size-4 border-[3px] border-warn bg-canvas`} />
    case 'gray':
      return <span className={`${base} size-4 bg-hairline`} />
    case 'tool':
      return <span className={`${base} mx-[5px] size-1.5 bg-ink-48/70`} />
    case 'lifecycle':
      return <span className={`${base} mx-1 size-2 bg-primary`} />
    default:
      return <span className={`${base} size-4 border-[3px] border-primary bg-canvas`} />
  }
}

export function EventTimeline({
  events,
  live,
}: {
  events: TaskEvent[]
  live: boolean
}) {
  return (
    <ol className="relative">
      {/* 竖线 */}
      <span
        aria-hidden
        className="absolute bottom-2 left-[7px] top-2 w-px bg-hairline"
      />
      {events.map((e) => {
        const kind = kindOf[e.type]
        const isTool = kind === 'tool'
        return (
          <li key={e.seq} className="flex gap-4 pb-5 last:pb-0">
            <Dot kind={kind} />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline justify-between gap-4">
                <p
                  className={
                    isTool
                      ? 'text-[13px] text-ink-80'
                      : 'text-[14px] font-semibold text-ink'
                  }
                >
                  {e.title}
                </p>
                <time className="shrink-0 text-[12px] tabular-nums text-ink-48">
                  {fmtClock(e.occurredAt)}
                </time>
              </div>
              {e.detail && (
                <p className="mt-0.5 text-[12px] leading-[1.5] text-ink-48">{e.detail}</p>
              )}
            </div>
          </li>
        )
      })}
      {live && (
        <li className="flex gap-4">
          <span className="relative z-10 mt-1 block size-4 shrink-0">
            <span className="absolute inset-0 animate-ping rounded-full bg-primary/30" />
            <span className="absolute inset-[3px] rounded-full bg-primary" />
          </span>
          <p className="text-[13px] text-ink-48">正在执行…</p>
        </li>
      )}
    </ol>
  )
}
