import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, LoaderCircle } from 'lucide-react'
import * as api from '@/shared/api'
import type {
  ConversationAnswerProvenance,
  TurnEvent,
  TurnToolActivity,
} from '@/shared/api/m1-types'
import { parseTurnToolActivity } from '@/shared/api/m1-types'
import { assistantProgressLabel, type AssistantProgressPhase } from './assistant-progress'

function sourceLabel(sourceType: ConversationAnswerProvenance['sources'][number]['sourceType']): string {
  if (sourceType === 'knowledge_chunk') return '企业知识库'
  if (sourceType === 'web') return '公开网络'
  return '会话附件'
}

function provenanceText(provenance: ConversationAnswerProvenance | undefined): string {
  if (!provenance) return '正在确认回答来源'
  const sourceSummary = provenance.sources.length > 0
    ? provenance.sources
        .map((source) => `${sourceLabel(source.sourceType)} ${source.count} 条`)
        .join(' · ')
    : ''
  if (provenance.executionPath === 'semantic_cache_hit') {
    const cache = provenance.cacheLayer === 'semantic' ? '语义缓存' : '回答缓存'
    return sourceSummary ? `${cache} · 原回答来源：${sourceSummary}` : `${cache} · 继承原回答来源`
  }
  if (sourceSummary) return sourceSummary
  if (provenance.toolCalls > 0) return `使用 ${provenance.toolCalls} 次工具 · 未形成外部引用`
  return '模型已有知识 · 未检索知识库或网络'
}

export function mergeTurnActivity(
  current: TurnToolActivity[],
  incoming: TurnToolActivity,
): TurnToolActivity[] {
  const index = current.findIndex((item) => item.activityId === incoming.activityId)
  if (index < 0) return [...current, incoming]
  const next = [...current]
  next[index] = { ...current[index], ...incoming }
  return next
}

export function activitiesFromEvents(events: TurnEvent[]): TurnToolActivity[] {
  return events.reduce<TurnToolActivity[]>((items, event) => {
    if (event.eventType !== 'turn_tool_started' && event.eventType !== 'turn_tool_completed') return items
    const activity = parseTurnToolActivity(event.payload)
    return activity ? mergeTurnActivity(items, activity) : items
  }, [])
}

async function loadTurnActivities(conversationId: string, turnId: string): Promise<TurnToolActivity[]> {
  const events: TurnEvent[] = []
  let cursor = 0
  for (let pageNumber = 0; pageNumber < 20; pageNumber += 1) {
    const page = await api.listTurnEvents(conversationId, turnId, cursor, 200)
    events.push(...page.items)
    cursor = page.nextAfterSeq
    if (!page.hasMore) break
  }
  return activitiesFromEvents(events)
}

function ActivityRow({ activity }: { activity: TurnToolActivity }) {
  const running = activity.status === 'running'
  return (
    <li className="relative pl-6">
      <span
        className={`absolute left-0 top-1.5 flex size-3 items-center justify-center rounded-full ${
          running
            ? 'bg-info-soft text-primary'
            : activity.status === 'succeeded'
              ? 'bg-success-soft text-success'
              : 'bg-danger-soft text-danger'
        }`}
        aria-hidden="true"
      >
        {running ? <LoaderCircle size={9} className="animate-spin" /> : <span className="size-1 rounded-full bg-current" />}
      </span>
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
        <span className="text-[12px] font-semibold text-ink">{activity.displayName}</span>
        <code className="text-[10px] text-ink-48">{activity.toolName}</code>
        {activity.attemptCount > 1 && (
          <span className="rounded-capsule bg-warning-soft px-1.5 py-0.5 text-[10px] font-medium text-warning">
            第 {activity.attemptCount} 次尝试
          </span>
        )}
        {activity.durationMillis > 0 && (
          <span className="ml-auto text-[10px] tabular-nums text-ink-48">
            {(activity.durationMillis / 1000).toFixed(activity.durationMillis >= 1000 ? 1 : 2)}s
          </span>
        )}
      </div>
      {activity.inputSummary && (
        <p className="mt-1 whitespace-pre-wrap break-words text-[11px] leading-[1.6] text-ink-48">
          {activity.inputSummary}
        </p>
      )}
      {activity.outputSummary && (
        <p className="mt-1 rounded-utility bg-pearl px-2.5 py-2 text-[11px] leading-[1.6] text-ink-80">
          <span className="font-semibold text-ink">结果：</span>{activity.outputSummary}
        </p>
      )}
    </li>
  )
}

function ProgressRow({ label, failed = false }: { label: string; failed?: boolean }) {
  return (
    <li className="relative pl-6">
      <span
        className={`absolute left-0 top-1 flex size-3 items-center justify-center rounded-full ${
          failed ? 'bg-danger-soft text-danger' : 'bg-info-soft text-primary'
        }`}
        aria-hidden="true"
      >
        {failed ? <span className="size-1 rounded-full bg-current" /> : <LoaderCircle size={9} className="animate-spin" />}
      </span>
      <p className={`text-[12px] font-medium ${failed ? 'text-danger' : 'text-ink-80'}`}>{label}</p>
    </li>
  )
}

function CompletedStep({ children }: { children: string }) {
  return (
    <li className="relative pl-6 text-[11px] text-ink-48">
      <span className="absolute left-1 top-1.5 size-1.5 rounded-full bg-success" aria-hidden="true" />
      {children}
    </li>
  )
}

export function AnswerActivity({
  conversationId,
  turnId,
  provenance,
  liveActivities,
  live = false,
  phase,
  hasOutput = false,
}: {
  conversationId: string
  turnId?: string
  provenance?: ConversationAnswerProvenance
  liveActivities?: TurnToolActivity[]
  live?: boolean
  phase?: AssistantProgressPhase
  hasOutput?: boolean
}) {
  const [open, setOpen] = useState(live)
  const history = useQuery({
    queryKey: ['conversation-turn-activities', conversationId, turnId],
    queryFn: () => loadTurnActivities(conversationId, turnId!),
    enabled: open && !live && Boolean(turnId),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  })
  const activities = liveActivities ?? history.data ?? []
  const currentProgress = phase
    ? assistantProgressLabel({ phase, activities, hasOutput })
    : null
  const canExpand = Boolean(phase) || live || Boolean(turnId && (provenance?.toolCalls ?? 0) > 0)
  const summary = currentProgress && !provenance
    ? currentProgress
    : provenanceText(provenance)
  const hasRunningActivity = activities.some((activity) => activity.status === 'running')

  return (
    <section className={hasOutput ? 'mt-3 border-t border-divider pt-2.5' : ''} aria-label="回答来源与处理过程">
      <div className="flex min-w-0 items-center gap-2">
        <span className={`size-1.5 shrink-0 rounded-full ${
          phase === 'failed'
            ? 'bg-danger'
            : phase || live
              ? 'animate-pulse bg-primary'
              : 'bg-success'
        }`} />
        <p className="min-w-0 flex-1 truncate text-[11px] font-medium text-ink-80" title={summary}>
          {phase || live ? summary : `本次回答：${summary}`}
        </p>
        {provenance && provenance.durationMillis > 0 && (
          <span className="shrink-0 text-[10px] tabular-nums text-ink-48">
            {(provenance.durationMillis / 1000).toFixed(1)}s
          </span>
        )}
        {canExpand && (
          <button
            type="button"
            className="press focus-ring inline-flex shrink-0 items-center gap-1 rounded-capsule px-1.5 py-1 text-[11px] font-semibold text-primary hover:bg-parchment"
            onClick={() => setOpen((value) => !value)}
            aria-expanded={open}
          >
            {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
            {open ? '收起进度' : '查看进度'}
          </button>
        )}
      </div>

      {open && (
        <div className="mt-3 rounded-utility border border-hairline bg-parchment/45 px-3 py-3">
          {history.isPending && !live ? (
            <p className="flex items-center gap-2 text-[11px] text-ink-48"><LoaderCircle size={13} className="animate-spin" />正在读取处理记录</p>
          ) : history.isError && !live ? (
            <p className="text-[11px] text-danger">处理记录暂时无法读取</p>
          ) : phase ? (
            <ol className="flex flex-col gap-3">
              {phase !== 'submitting' && <CompletedStep>已接收问题</CompletedStep>}
              {activities.map((activity) => <ActivityRow key={activity.activityId} activity={activity} />)}
              {!hasRunningActivity && currentProgress && (
                <ProgressRow label={currentProgress} failed={phase === 'failed'} />
              )}
            </ol>
          ) : activities.length > 0 ? (
            <ol className="flex flex-col gap-3">
              {activities.map((activity) => <ActivityRow key={activity.activityId} activity={activity} />)}
            </ol>
          ) : (
            <p className="text-[11px] leading-[1.6] text-ink-48">
              {provenance?.toolCalls
                ? `该历史回答记录了 ${provenance.toolCalls} 次工具调用，但生成时尚未保存可展示的调用摘要。`
                : '本次回答没有调用外部工具。'}
            </p>
          )}
        </div>
      )}
    </section>
  )
}
