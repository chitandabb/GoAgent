import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as api from '@/shared/api'
import type { ConversationCitation, ConversationMessage } from '@/shared/api/m1-types'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { Spinner } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'
import { AttachmentPreviewDialog, useAttachmentPreview } from '@/shared/ui/AttachmentPreview'
import { MessageBubble, fmtBytes } from './MessageBubble'

type TurnPhase = 'submitting' | 'queued' | 'running' | 'retry' | 'failed'

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
}

const turnPhaseMeta: Record<TurnPhase, { label: string; tone: 'gray' | 'info' | 'warn' }> = {
  submitting: { label: '发送中…', tone: 'gray' },
  queued: { label: '排队中', tone: 'gray' },
  running: { label: '助手思考中', tone: 'info' },
  retry: { label: '重试中', tone: 'warn' },
  failed: { label: '生成失败', tone: 'warn' },
}

function knowledgeChunkIdFromRef(sourceRef: string): string | null {
  const parts = sourceRef.split('/')
  const chunkId = parts[parts.length - 1]
  return chunkId && chunkId.length > 0 ? chunkId : null
}

export function AssistantPage() {
  const qc = useQueryClient()
  const toast = useToast()
  const [activeId, setActiveId] = useState<string | null>(null)
  const [input, setInput] = useState('')
  const [uploads, setUploads] = useState<UploadEntry[]>([])
  const [turn, setTurn] = useState<{ turnId: string; phase: TurnPhase; failure?: string } | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const unsubscribeRef = useRef<(() => void) | null>(null)
  const lastTurnRef = useRef<LastTurn | null>(null)
  const preview = useAttachmentPreview()

  const conversations = useQuery({
    queryKey: ['conversations'],
    queryFn: () => api.listConversations(1, 50),
  })
  const conversationList = conversations.data?.items ?? []

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
  }, [activeId])

  const messages = useQuery({
    queryKey: ['conversation-messages', activeId],
    queryFn: () => (activeId ? api.listConversationMessages(activeId) : Promise.resolve([])),
    enabled: activeId !== null,
  })
  const orderedMessages = [...(messages.data ?? [])].sort((a, b) => a.seq - b.seq)

  // 新消息 / turn 状态变化时自动滚到底部
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [orderedMessages, turn])

  const createConv = useMutation({
    mutationFn: () => api.createConversation(''),
    onSuccess: (conv) => {
      qc.invalidateQueries({ queryKey: ['conversations'] })
      setActiveId(conv.id)
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '创建会话失败'),
  })

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

  const submitTurn = async (content: string, attachments: { attachmentId: string }[]) => {
    if (!activeId || turn) return
    setTurn({ turnId: '', phase: 'submitting' })
    try {
      const result = await api.appendTurn(activeId, { content, attachments }, api.createIdempotencyKey())
      const turnId = result.turnId
      setTurn({
        turnId,
        phase: result.status === 'failed' ? 'failed' : result.status === 'retry_scheduled' ? 'retry' : 'running',
      })
      // 乐观插入用户消息
      qc.setQueryData<ConversationMessage[]>(['conversation-messages', activeId], (current = []) => [
        ...current,
        result.userMessage,
      ])
      void qc.invalidateQueries({ queryKey: ['conversations'] })

      unsubscribeRef.current?.()
      unsubscribeRef.current = api.subscribeTurnEvents(activeId, turnId, {
        onEvent: (event) => {
          setTurn((current) => {
            if (!current || current.turnId !== turnId) return current
            switch (event.eventType) {
              case 'turn_queued':
                return { ...current, phase: 'queued' }
              case 'turn_running':
                return { ...current, phase: 'running' }
              case 'turn_retry_scheduled':
                return { ...current, phase: 'retry' }
              case 'turn_failed':
                return { ...current, phase: 'failed' }
              default:
                return current
            }
          })
        },
        onStatus: () => {},
        onTerminal: (finalEvent) => {
          const failed = finalEvent?.eventType === 'turn_failed'
          if (failed) {
            void api
              .getTurn(activeId, turnId)
              .then((detail) =>
                setTurn({
                  turnId,
                  phase: 'failed',
                  failure: detail.failureSummary || '请稍后重试',
                }),
              )
              .catch(() => setTurn({ turnId, phase: 'failed', failure: '生成失败' }))
          } else {
            setTurn(null)
          }
          void qc.invalidateQueries({ queryKey: ['conversation-messages', activeId] })
          void qc.invalidateQueries({ queryKey: ['conversations'] })
        },
        onError: (error) => {
          setTurn((current) =>
            current && current.turnId === turnId
              ? { ...current, phase: 'failed', failure: error.message }
              : current,
          )
        },
      })
    } catch (error) {
      setTurn(null)
      toast.error(error instanceof Error ? error.message : '发送失败')
    }
  }

  const send = async () => {
    if (!activeId || !input.trim() || turn) return
    const text = input.trim()
    const pending = uploads
    if (pending.some((entry) => entry.status !== 'ready')) return
    setInput('')
    setUploads([])
    const attachments = pending
      .filter((entry): entry is UploadEntry & { attachmentId: string } => Boolean(entry.attachmentId))
      .map((entry) => ({ attachmentId: entry.attachmentId }))
    lastTurnRef.current = { content: text, attachments }
    void submitTurn(text, attachments)
  }

  const retryTurn = () => {
    const last = lastTurnRef.current
    if (!activeId || !last || turn) return
    void submitTurn(last.content, last.attachments)
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
      uploads.every((entry) => entry.status === 'ready'),
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
            conversationList.map((conv) => (
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
            ))
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
              {orderedMessages.map((message) => (
                <MessageBubble key={message.id} message={message} onCitationClick={openCitation} />
              ))}
              {turn && (
                <div className="flex flex-col items-start">
                  <div className="flex max-w-[85%] items-center gap-2.5 rounded-[18px] rounded-bl-[6px] border border-hairline bg-canvas px-5 py-3.5">
                    {turn.phase !== 'failed' ? (
                      <>
                        <Spinner />
                        <span className="text-[13px] text-ink-80">{turnPhaseMeta[turn.phase].label}</span>
                      </>
                    ) : (
                      <Badge tone="red">{turnPhaseMeta[turn.phase].label}</Badge>
                    )}
                    {turn.phase === 'failed' && turn.failure && (
                      <span className="max-w-[420px] text-[12px] text-danger">{turn.failure}</span>
                    )}
                    {turn.phase === 'failed' && lastTurnRef.current && (
                      <Button size="sm" variant="ghost" onClick={retryTurn}>
                        重试
                      </Button>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>

        {/* 输入区 */}
        <div className="border-t border-divider p-4">
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
                  >
                    ✕
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
