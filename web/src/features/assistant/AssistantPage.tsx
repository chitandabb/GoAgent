import { useCallback, useEffect, useRef, useState } from 'react'
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { useSearchParams } from 'react-router'
import * as api from '@/shared/api'
import type {
  ConversationCaseReference,
  ConversationCitation,
  ConversationMessage,
} from '@/shared/api/m1-types'
import { parseTurnMessageDelta } from '@/shared/api/m1-types'
import { fmtDateTime } from '@/shared/lib/fmt'
import { knowledgeDocumentFileAccept } from '@/shared/lib/knowledge-upload'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { Spinner } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'
import { AttachmentPreviewDialog, useAttachmentPreview } from '@/shared/ui/AttachmentPreview'
import { MessageBubble, fmtBytes } from './MessageBubble'
import { clearActiveTurn, loadActiveTurn, saveActiveTurn } from './turn-store'

type TurnPhase = 'submitting' | 'queued' | 'running' | 'retry' | 'reconnecting' | 'finalizing' | 'failed'

interface UploadEntry {
  key: string
  file: File
  status: 'uploading' | 'ready' | 'error'
  attachmentId?: string
  error?: string
}

interface LastTurn {
  content: string
  attachments: { attachmentId: string }[]
  caseReferences: ConversationCaseReference[]
}

interface ActiveTurn {
  turnId: string
  phase: TurnPhase
  failure?: string
  assistantMessageId?: string
  streamed: string
  connectionLost?: boolean
}

const turnPhaseMeta: Record<TurnPhase, { label: string; tone: 'gray' | 'info' | 'warn' }> = {
  submitting: { label: '发送中…', tone: 'gray' },
  queued: { label: '排队中', tone: 'gray' },
  running: { label: '助手思考中', tone: 'info' },
  retry: { label: '系统重试中', tone: 'warn' },
  reconnecting: { label: '正在恢复连接', tone: 'warn' },
  finalizing: { label: '整理回答…', tone: 'gray' },
  failed: { label: '生成失败', tone: 'warn' },
}

/** 回答内容被服务端按 40 rune 分块推送；收到即整体可见，这里用逐字动画平滑呈现。 */
function useTypewriterReveal(target: string, active: boolean, resetKey: string): number {
  const [revealed, setRevealed] = useState(0)
  useEffect(() => {
    setRevealed(0)
  }, [resetKey])
  useEffect(() => {
    if (!active) return
    const totalRunes = [...target].length
    if (revealed >= totalRunes) return
    const frame = requestAnimationFrame(() => {
      setRevealed((current) => {
        const remaining = totalRunes - current
        const step = Math.max(4, Math.ceil(remaining / 12))
        return Math.min(totalRunes, current + step)
      })
    })
    return () => cancelAnimationFrame(frame)
  }, [active, target, revealed])
  return active ? Math.min(revealed, [...target].length) : 0
}

function knowledgeChunkIdFromRef(sourceRef: string): string | null {
  const parts = sourceRef.split('/')
  const chunkId = parts[parts.length - 1]
  return chunkId && chunkId.length > 0 ? chunkId : null
}

function joinedDeltaChunks(chunks: Map<number, string> | null): string {
  if (!chunks || chunks.size === 0) return ''
  return [...chunks.keys()]
    .sort((a, b) => a - b)
    .map((position) => chunks.get(position) ?? '')
    .join('')
}

export function AssistantPage() {
  const qc = useQueryClient()
  const toast = useToast()
  const [searchParams, setSearchParams] = useSearchParams()
  const requestedCaseId = searchParams.get('caseId')
  const [activeId, setActiveId] = useState<string | null>(null)
  const [selectedCaseId, setSelectedCaseId] = useState<string | null>(() => requestedCaseId)
  const [input, setInput] = useState('')
  const [uploads, setUploads] = useState<UploadEntry[]>([])
  const [turn, setTurn] = useState<ActiveTurn | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const unsubscribeRef = useRef<(() => void) | null>(null)
  const lastTurnRef = useRef<LastTurn | null>(null)
  const autoCreatedForCaseRef = useRef<string | null>(null)
  // delta 分块按 position 累积，重连/回放重发同一 position 时天然去重
  const deltaChunksRef = useRef<Map<number, string> | null>(null)
  const preview = useAttachmentPreview()

  const turnStreaming = turn !== null && turn.phase !== 'failed'
  const revealedRunes = useTypewriterReveal(turn?.streamed ?? '', turnStreaming, turn?.turnId ?? '')

  const subscribeToTurn = useCallback((conversationId: string, turnId: string) => {
    unsubscribeRef.current?.()
    unsubscribeRef.current = api.subscribeTurnEvents(conversationId, turnId, {
      onEvent: (event) => {
        if (event.eventType === 'turn_message_delta') {
          const delta = parseTurnMessageDelta(event.payload)
          if (!delta) return
          const chunks = deltaChunksRef.current ?? new Map<number, string>()
          deltaChunksRef.current = chunks
          chunks.set(delta.position, delta.content)
          setTurn((current) =>
            current && current.turnId === turnId
              ? {
                  ...current,
                  streamed: joinedDeltaChunks(chunks),
                  assistantMessageId: delta.messageId,
                  connectionLost: false,
                }
              : current,
          )
          return
        }
        setTurn((current) => {
          if (!current || current.turnId !== turnId) return current
          switch (event.eventType) {
            case 'turn_queued':
              return { ...current, phase: 'queued', failure: undefined, connectionLost: false }
            case 'turn_running':
              return { ...current, phase: 'running', failure: undefined, connectionLost: false }
            case 'turn_retry_scheduled':
              return { ...current, phase: 'retry', failure: undefined, connectionLost: false }
            case 'turn_failed':
              return { ...current, phase: 'failed', connectionLost: false }
            default:
              return current
          }
        })
      },
      onStatus: (status) => {
        if (status === 'reconnecting') {
          setTurn((current) =>
            current && current.turnId === turnId
              ? { ...current, phase: 'reconnecting', connectionLost: false }
              : current,
          )
          return
        }
        if (status === 'connected') {
          setTurn((current) =>
            current && current.turnId === turnId && current.phase === 'reconnecting'
              ? { ...current, phase: 'running', failure: undefined, connectionLost: false }
              : current,
          )
          return
        }
        if (status !== 'failed') return
        void api.getTurn(conversationId, turnId).then((detail) => {
          if (detail.status === 'completed') {
            clearActiveTurn(conversationId, turnId)
            setTurn((current) => current?.turnId === turnId ? null : current)
            void qc.invalidateQueries({ queryKey: ['conversation-messages', conversationId] })
            return
          }
          if (detail.status === 'failed') {
            setTurn((current) =>
              current && current.turnId === turnId
                ? {
                    ...current,
                    phase: 'failed',
                    failure: detail.failureSummary || '请检查输入后重试',
                    connectionLost: false,
                  }
                : current,
            )
            return
          }
          setTurn((current) =>
            current && current.turnId === turnId
              ? {
                  ...current,
                  phase: 'reconnecting',
                  failure: '实时事件连接已停止，任务仍在服务端执行。',
                  connectionLost: true,
                }
              : current,
          )
        }).catch(() => {
          setTurn((current) =>
            current && current.turnId === turnId
              ? {
                  ...current,
                  phase: 'reconnecting',
                  failure: '暂时无法确认生成状态，请重新连接。',
                  connectionLost: true,
                }
              : current,
          )
        })
      },
      onTerminal: (finalEvent) => {
        const failed = finalEvent?.eventType === 'turn_failed'
        if (failed) {
          void api.getTurn(conversationId, turnId).then((detail) =>
            setTurn((current) =>
              current && current.turnId === turnId
                ? {
                    ...current,
                    phase: 'failed',
                    failure: detail.failureSummary || '请检查输入后重试',
                    connectionLost: false,
                  }
                : current,
            ),
          ).catch(() =>
            setTurn((current) =>
              current && current.turnId === turnId
                ? { ...current, phase: 'failed', failure: '生成失败', connectionLost: false }
                : current,
            ),
          )
        } else {
          clearActiveTurn(conversationId, turnId)
          setTurn((current) =>
            current && current.turnId === turnId ? { ...current, phase: 'finalizing' } : current,
          )
        }
        void qc.invalidateQueries({ queryKey: ['conversation-messages', conversationId] })
        void qc.invalidateQueries({ queryKey: ['conversations'] })
      },
      onError: (error) => {
        setTurn((current) =>
          current && current.turnId === turnId && current.phase === 'reconnecting'
            ? { ...current, failure: error.message }
            : current,
        )
      },
    })
  }, [qc])

  const conversations = useInfiniteQuery({
    queryKey: ['conversations'],
    queryFn: ({ pageParam }) => api.listConversations(pageParam, 50),
    initialPageParam: 1,
    getNextPageParam: (lastPage) => {
      const totalPages = Math.max(1, Math.ceil(lastPage.total / lastPage.pageSize))
      return lastPage.page < totalPages ? lastPage.page + 1 : undefined
    },
  })
  const conversationList = conversations.data?.pages.flatMap((page) => page.items) ?? []
  const selectedCase = useQuery({
    queryKey: ['external-case', selectedCaseId],
    queryFn: () => api.getExternalCase(selectedCaseId!),
    enabled: Boolean(selectedCaseId),
    retry: false,
  })

  useEffect(() => {
    if (requestedCaseId) setSelectedCaseId(requestedCaseId)
  }, [requestedCaseId])

  // 默认选中最近会话
  useEffect(() => {
    if (!activeId && conversationList.length > 0) setActiveId(conversationList[0].id)
  }, [activeId, conversationList])

  // 切换会话 / 卸载时停止旧 turn 订阅，并清理属于旧会话的附件与 turn 状态
  useEffect(() => {
    return () => {
      unsubscribeRef.current?.()
      unsubscribeRef.current = null
    }
  }, [activeId])

  useEffect(() => {
    setUploads([])
    setTurn(null)
    lastTurnRef.current = null
    deltaChunksRef.current = null
  }, [activeId])

  // 刷新或返回工作台后，从服务端状态和可回放事件恢复仍在执行的回合。
  useEffect(() => {
    if (!activeId) return
    const stored = loadActiveTurn(activeId)
    if (!stored) return
    let mounted = true
    lastTurnRef.current = stored.input
    deltaChunksRef.current = new Map()
    void api.getTurn(activeId, stored.turnId).then((detail) => {
      if (!mounted) return
      if (detail.status === 'completed') {
        clearActiveTurn(activeId, stored.turnId)
        void qc.invalidateQueries({ queryKey: ['conversation-messages', activeId] })
        return
      }
      const phase: TurnPhase = detail.status === 'queued'
        ? detail.retryAt ? 'retry' : 'queued'
        : detail.status === 'running'
          ? 'running'
          : 'failed'
      setTurn({
        turnId: stored.turnId,
        phase,
        failure: detail.failureSummary || undefined,
        streamed: '',
      })
      if (detail.status !== 'failed') subscribeToTurn(activeId, stored.turnId)
    }).catch((error) => {
      if (!mounted) return
      setTurn({
        turnId: stored.turnId,
        phase: 'reconnecting',
        failure: error instanceof Error ? error.message : '暂时无法恢复生成状态，请重新连接。',
        streamed: '',
        connectionLost: true,
      })
    })
    return () => {
      mounted = false
    }
  }, [activeId, qc, subscribeToTurn])

  const messages = useQuery({
    queryKey: ['conversation-messages', activeId],
    queryFn: () => (activeId ? api.listConversationMessages(activeId) : Promise.resolve([])),
    enabled: activeId !== null,
  })
  const orderedMessages = [...(messages.data ?? [])].sort((a, b) => a.seq - b.seq)

  // 新消息 / 流式内容推进时自动滚到底部
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [orderedMessages, turn, revealedRunes])

  // finalizing：逐字动画追平且持久化助手消息回填后，无缝移除流式气泡；5 秒兜底
  useEffect(() => {
    if (!turn || turn.phase !== 'finalizing') return
    if (turn.assistantMessageId) {
      const caughtUp = revealedRunes >= [...turn.streamed].length
      const settled = orderedMessages.some((message) => message.id === turn.assistantMessageId)
      if (caughtUp && settled) {
        setTurn(null)
        return
      }
    }
    const timer = window.setTimeout(() => setTurn(null), 5000)
    return () => window.clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turn, revealedRunes, orderedMessages])

  const createConv = useMutation({
    mutationFn: () => api.createConversation(''),
    onSuccess: (conv) => {
      qc.invalidateQueries({ queryKey: ['conversations'] })
      setActiveId(conv.id)
    },
    onError: (error) => {
      autoCreatedForCaseRef.current = null
      toast.error(error instanceof Error ? error.message : '创建会话失败')
    },
  })

  useEffect(() => {
    if (
      !requestedCaseId ||
      activeId ||
      conversations.isPending ||
      conversationList.length > 0 ||
      createConv.isPending ||
      autoCreatedForCaseRef.current === requestedCaseId
    ) return
    autoCreatedForCaseRef.current = requestedCaseId
    createConv.mutate()
  }, [activeId, conversationList.length, conversations.isPending, createConv, requestedCaseId])

  const uploadOne = async (key: string, file: File) => {
    if (!activeId) return
    try {
      const attachment = await api.uploadConversationAttachment(activeId, file, api.createIdempotencyKey())
      setUploads((current) =>
        current.map((entry) =>
          entry.key === key
            ? { ...entry, status: 'ready', attachmentId: attachment.attachmentId }
            : entry,
        ),
      )
    } catch (error) {
      setUploads((current) =>
        current.map((entry) =>
          entry.key === key
            ? { ...entry, status: 'error', error: error instanceof Error ? error.message : '上传失败' }
            : entry,
        ),
      )
    }
  }

  const onFilesChosen = (files: FileList | null) => {
    if (!activeId || !files) return
    Array.from(files).forEach((file) => {
      const key = api.createIdempotencyKey()
      setUploads((current) => [...current, { key, file, status: 'uploading' }])
      void uploadOne(key, file)
    })
  }

  const submitTurn = async (
    content: string,
    attachments: { attachmentId: string }[],
    caseReferences: ConversationCaseReference[],
    force = false,
  ) => {
    if (!activeId || (turn && !force)) return
    const conversationId = activeId
    deltaChunksRef.current = new Map()
    setTurn({ turnId: '', phase: 'submitting', streamed: '' })
    try {
      const result = await api.appendTurn(
        conversationId,
        { content, attachments, caseReferences },
        api.createIdempotencyKey(),
      )
      const turnId = result.turnId
      const submitted = { content, attachments, caseReferences }
      lastTurnRef.current = submitted
      saveActiveTurn(conversationId, { turnId, input: submitted })
      setTurn({
        turnId,
        phase: result.status === 'queued' ? 'queued' : result.status === 'failed' ? 'failed' : 'running',
        streamed: '',
      })
      // 乐观插入用户消息
      qc.setQueryData<ConversationMessage[]>(['conversation-messages', conversationId], (current = []) => [
        ...current,
        result.userMessage,
      ])
      void qc.invalidateQueries({ queryKey: ['conversations'] })
      if (result.status !== 'failed') subscribeToTurn(conversationId, turnId)
    } catch (error) {
      setTurn(null)
      toast.error(error instanceof Error ? error.message : '发送失败')
    }
  }

  const send = async () => {
    if (!activeId || !input.trim() || turn) return
    if (selectedCaseId && !selectedCase.data) {
      toast.error(selectedCase.isError ? '当前工单不可访问' : '正在读取当前工单，请稍候')
      return
    }
    const text = input.trim()
    const pending = uploads
    if (pending.some((entry) => entry.status !== 'ready')) return
    setInput('')
    setUploads([])
    const attachments = pending
      .filter((entry): entry is UploadEntry & { attachmentId: string } => Boolean(entry.attachmentId))
      .map((entry) => ({ attachmentId: entry.attachmentId }))
    const caseReferences: ConversationCaseReference[] = selectedCaseId
      ? [{ externalCaseId: selectedCaseId, kind: 'selected' }]
      : []
    lastTurnRef.current = { content: text, attachments, caseReferences }
    void submitTurn(text, attachments, caseReferences)
  }

  const retryTurn = () => {
    const last = lastTurnRef.current
    if (!activeId || !last || !turn || turn.phase !== 'failed') return
    unsubscribeRef.current?.()
    clearActiveTurn(activeId, turn.turnId)
    setTurn(null)
    void submitTurn(last.content, last.attachments, last.caseReferences, true)
  }

  const reconnectTurn = () => {
    if (!activeId || !turn || !turn.connectionLost) return
    setTurn((current) => current ? { ...current, failure: undefined, connectionLost: false } : current)
    subscribeToTurn(activeId, turn.turnId)
  }

  const clearSelectedCase = () => {
    setSelectedCaseId(null)
    const next = new URLSearchParams(searchParams)
    next.delete('caseId')
    setSearchParams(next, { replace: true })
  }

  const openCitation = (citation: ConversationCitation) => {
    if (citation.sourceType === 'web') return
    if (citation.sourceType === 'attachment') {
      const attachmentId = citation.sourceRef.replace(/^attachment:/, '')
      if (!attachmentId || !activeId) return
      preview.openPreview({
        kind: 'attachment',
        conversationId: activeId,
        attachmentId,
        name: '附件引用',
      })
      return
    }
    const chunkId = knowledgeChunkIdFromRef(citation.sourceRef.replace(/^knowledge:/, ''))
    if (chunkId) preview.openPreview({ kind: 'knowledge', chunkId })
  }

  const readyToSend = Boolean(
    activeId &&
      input.trim() &&
      uploads.every((entry) => entry.status === 'ready') &&
      (!selectedCaseId || selectedCase.data),
  )

  return (
    <div className="flex h-[calc(100dvh-11.5rem)] min-h-[520px] gap-5">
      {/* 会话列表 */}
      <Card className="flex w-60 shrink-0 flex-col overflow-hidden">
        <div className="border-b border-divider p-3">
          <Button
            variant="neutral"
            size="sm"
            className="w-full"
            onClick={() => createConv.mutate()}
            disabled={createConv.isPending}
          >
            {createConv.isPending ? '创建中…' : '+ 新对话'}
          </Button>
        </div>
        <div className="flex-1 overflow-y-auto p-2">
          {conversations.isPending ? (
            <div className="flex justify-center py-8">
              <Spinner />
            </div>
          ) : conversationList.length === 0 ? (
            <p className="px-3 py-8 text-center text-[12px] leading-[1.6] text-ink-48">
              还没有会话
              <br />
              点击上方"新对话"开始
            </p>
          ) : (
            <>
              {conversationList.map((conv) => (
                <button
                  key={conv.id}
                  type="button"
                  onClick={() => setActiveId(conv.id)}
                  className={`press mb-1 block w-full rounded-capsule px-3 py-2.5 text-left ${
                    conv.id === activeId ? 'bg-parchment' : 'hover:bg-pearl'
                  }`}
                >
                  <p className="line-clamp-1 text-[13px] font-semibold text-ink">
                    {conv.title || '未命名对话'}
                  </p>
                  <p className="mt-0.5 text-[11px] text-ink-48">
                    {fmtDateTime(conv.updatedAt)}
                  </p>
                </button>
              ))}
              {conversations.hasNextPage && (
                <button
                  type="button"
                  className="press mt-1 block w-full rounded-capsule px-3 py-2 text-center text-[12px] text-primary hover:bg-pearl disabled:opacity-45"
                  disabled={conversations.isFetchingNextPage}
                  onClick={() => void conversations.fetchNextPage()}
                >
                  {conversations.isFetchingNextPage ? '加载中…' : `加载更多（共 ${conversations.data?.pages[0].total ?? 0} 个会话）`}
                </button>
              )}
            </>
          )}
        </div>
        <div className="border-t border-divider px-3 py-2.5 text-[11px] leading-[1.6] text-ink-48">
          会话与服务端同步；回答可引用企业知识库与工单上下文，点击引用可查看原文片段。
        </div>
      </Card>

      {/* 对话区 */}
      <Card className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <div ref={scrollRef} className="flex-1 overflow-y-auto px-6 py-6">
          {orderedMessages.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
              {messages.isError ? (
                <>
                  <p className="text-[15px] font-semibold text-ink">消息读取失败</p>
                  <p className="max-w-md text-[13px] leading-[1.7] text-ink-48">
                    {messages.error instanceof Error ? messages.error.message : '请稍后重试'}
                  </p>
                  <Button size="sm" variant="neutral" onClick={() => void messages.refetch()}>
                    重新加载
                  </Button>
                </>
              ) : (
                <>
                  <p className="text-[17px] font-semibold text-ink">知识助手</p>
                  <p className="max-w-md text-[13px] leading-[1.7] text-ink-48">
                    解释术语、产品功能与常见问题，也可结合你上传的截图、日志与工单上下文分析。
                    企业知识库优先；回答附引用来源。
                  </p>
                </>
              )}
            </div>
          ) : (
            <div className="flex flex-col gap-5">
              {orderedMessages
                .filter((message) => message.id !== turn?.assistantMessageId)
                .map((message) => (
                  <MessageBubble key={message.id} message={message} onCitationClick={openCitation} />
                ))}
              {turn && (
                <div className="flex flex-col items-start">
                  {turn.streamed.length > 0 ? (
                    <div className="max-w-[85%] rounded-[18px] rounded-bl-[6px] border border-hairline bg-canvas px-5 py-4">
                      <p className="whitespace-pre-wrap text-[14px] leading-[1.7] text-ink">
                        {[...turn.streamed].slice(0, turn.phase === 'failed' ? undefined : revealedRunes).join('')}
                        {turn.phase !== 'failed' && (
                          <span className="ml-0.5 inline-block h-[15px] w-[7px] translate-y-[2px] animate-pulse rounded-[1px] bg-ink-48" />
                        )}
                      </p>
                    </div>
                  ) : null}
                  <div className="mt-2 flex max-w-[85%] items-center gap-2.5">
                    {turn.connectionLost ? (
                      <>
                        <Badge tone="red">连接已断开</Badge>
                        {turn.failure && (
                          <span className="max-w-[420px] text-[12px] text-danger">{turn.failure}</span>
                        )}
                        <Button size="sm" variant="ghost" onClick={reconnectTurn}>
                          重新连接
                        </Button>
                      </>
                    ) : turn.phase !== 'failed' ? (
                      turn.streamed.length === 0 && (
                        <>
                          <Spinner />
                          <span className="text-[13px] text-ink-80">{turnPhaseMeta[turn.phase].label}</span>
                        </>
                      )
                    ) : (
                      <>
                        <Badge tone="red">{turnPhaseMeta[turn.phase].label}</Badge>
                        {turn.failure && (
                          <span className="max-w-[420px] text-[12px] text-danger">{turn.failure}</span>
                        )}
                        {lastTurnRef.current && (
                          <Button size="sm" variant="ghost" onClick={retryTurn}>
                            重试
                          </Button>
                        )}
                      </>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        {/* 输入区 */}
        <div className="border-t border-divider p-4">
          {selectedCaseId && (
            <div className="mb-2.5 flex items-center gap-2 rounded-capsule bg-parchment px-3 py-1.5 text-[12px] text-ink-80">
              <span className="font-semibold text-ink">当前工单</span>
              {selectedCase.isPending ? (
                <Spinner />
              ) : selectedCase.data ? (
                <span className="min-w-0 truncate">
                  {selectedCase.data.externalCaseKey} · {selectedCase.data.title}
                </span>
              ) : (
                <span className="text-danger">工单不可访问</span>
              )}
              <button
                type="button"
                className="press focus-ring ml-auto inline-flex size-6 shrink-0 items-center justify-center rounded-full text-ink-48 hover:bg-canvas hover:text-danger"
                onClick={clearSelectedCase}
                aria-label="移除当前工单"
                title="移除当前工单"
              >
                <X size={14} aria-hidden="true" />
              </button>
            </div>
          )}
          {uploads.length > 0 && (
            <div className="mb-2.5 flex flex-wrap gap-2">
              {uploads.map((entry) => (
                <span
                  key={entry.key}
                  className="inline-flex items-center gap-2 rounded-capsule bg-pearl px-3 py-1.5 text-[12px] text-ink-80"
                >
                  <span className="max-w-[180px] truncate">{entry.file.name}</span>
                  <span className="text-ink-48">{fmtBytes(entry.file.size)}</span>
                  {entry.status === 'uploading' && <Spinner />}
                  {entry.status === 'ready' && <Badge tone="blue">已就绪</Badge>}
                  {entry.status === 'error' && (
                    <span title={entry.error}>
                      <Badge tone="red">上传失败</Badge>
                    </span>
                  )}
                  <button
                    type="button"
                    className="press text-ink-48 hover:text-danger"
                    onClick={() => setUploads((current) => current.filter((item) => item.key !== entry.key))}
                    aria-label="移除附件"
                    title="移除附件"
                  >
                    <X size={14} aria-hidden="true" />
                  </button>
                </span>
              ))}
            </div>
          )}

          <div className="flex items-end gap-2.5">
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={!activeId || Boolean(turn)}
              className="press focus-ring flex size-10 shrink-0 items-center justify-center rounded-full border border-hairline bg-canvas text-[18px] text-ink-80 hover:bg-pearl disabled:opacity-40"
              aria-label="添加附件"
            >
              +
            </button>
            <input
              ref={fileInputRef}
              type="file"
              accept={knowledgeDocumentFileAccept}
              multiple
              className="hidden"
              onChange={(event) => {
                onFilesChosen(event.target.files)
                event.target.value = ''
              }}
            />

            <textarea
              value={input}
              onChange={(event) => setInput(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && !event.shiftKey) {
                  event.preventDefault()
                  void send()
                }
              }}
              rows={1}
              placeholder="输入问题，Enter 发送，Shift+Enter 换行"
              className="focus-ring max-h-32 min-h-10 flex-1 resize-none rounded-[20px] border border-hairline bg-canvas px-4 py-2.5 text-[14px] leading-[1.5] text-ink"
            />

            <Button onClick={() => void send()} disabled={!readyToSend || Boolean(turn)}>
              {turn ? '生成中…' : '发送'}
            </Button>
          </div>

          <div className="mt-2.5 flex items-center justify-between">
            <span className="text-[11px] text-ink-48">
              支持截图、日志、PDF 与文本附件；附件仅对当前会话可见
            </span>
          </div>
        </div>
      </Card>

      <AttachmentPreviewDialog preview={preview.preview} onClose={preview.closePreview} />
    </div>
  )
}
