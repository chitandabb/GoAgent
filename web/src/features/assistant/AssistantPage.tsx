import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as api from '@/shared/api'
import type { ChatMessage, MessageAttachment } from '@/shared/api'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card } from '@/shared/ui/Card'
import { Spinner } from '@/shared/ui/Spinner'
import { MessageBubble } from './MessageBubble'

const demoAttachmentNames = ['报错截图-0726.png', '现场日志片段.log', '设置界面截图.png']

export function AssistantPage() {
  const qc = useQueryClient()
  const [activeId, setActiveId] = useState<string | null>(null)
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [input, setInput] = useState('')
  const [webSearch, setWebSearch] = useState(false)
  const [pendingAttachments, setPendingAttachments] = useState<MessageAttachment[]>([])
  const [attachMenuOpen, setAttachMenuOpen] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)

  const conversations = useQuery({
    queryKey: ['conversations'],
    queryFn: api.listConversations,
  })

  // 默认选中最近会话
  useEffect(() => {
    if (!activeId && conversations.data && conversations.data.length > 0) {
      setActiveId(conversations.data[0].conversationId)
    }
  }, [activeId, conversations.data])

  // 订阅当前会话（模拟流式 SSE）
  useEffect(() => {
    if (!activeId) return
    setMessages([])
    const unsubscribe = api.subscribeConversation(activeId, setMessages)
    return unsubscribe
  }, [activeId])

  // 新消息自动滚到底部
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [messages])

  const streaming = messages.some((m) => m.status === 'streaming')

  const createConv = useMutation({
    mutationFn: api.createConversation,
    onSuccess: (conv) => {
      qc.invalidateQueries({ queryKey: ['conversations'] })
      setActiveId(conv.conversationId)
    },
  })

  const send = async () => {
    if (!activeId || !input.trim() || streaming) return
    const text = input.trim()
    setInput('')
    const atts = pendingAttachments
    setPendingAttachments([])
    await api.sendAssistantMessage(activeId, text, atts, webSearch)
    qc.invalidateQueries({ queryKey: ['conversations'] })
  }

  const addAttachment = (scope: MessageAttachment['scope']) => {
    const name =
      demoAttachmentNames[pendingAttachments.length % demoAttachmentNames.length]
    setPendingAttachments([...pendingAttachments, { name, scope }])
    setAttachMenuOpen(false)
  }

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
          >
            + 新对话
          </Button>
        </div>
        <div className="flex-1 overflow-y-auto p-2">
          {conversations.isPending ? (
            <div className="flex justify-center py-8">
              <Spinner />
            </div>
          ) : (
            (conversations.data ?? []).map((c) => (
              <button
                key={c.conversationId}
                type="button"
                onClick={() => setActiveId(c.conversationId)}
                className={`press mb-1 block w-full rounded-capsule px-3 py-2.5 text-left ${
                  c.conversationId === activeId ? 'bg-parchment' : 'hover:bg-pearl'
                }`}
              >
                <p className="line-clamp-1 text-[13px] font-semibold text-ink">
                  {c.title}
                </p>
                <p className="mt-0.5 text-[11px] text-ink-48">
                  {fmtDateTime(c.updatedAt)}
                </p>
              </button>
            ))
          )}
        </div>
        <div className="border-t border-divider px-3 py-2.5 text-[11px] leading-[1.6] text-ink-48">
          知识助手的回答不会自动成为诊断证据；工单问题请使用工单诊断。
        </div>
      </Card>

      {/* 对话区 */}
      <Card className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <div ref={scrollRef} className="flex-1 overflow-y-auto px-6 py-6">
          {messages.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
              <p className="text-[17px] font-semibold text-ink">知识助手</p>
              <p className="max-w-md text-[13px] leading-[1.7] text-ink-48">
                解释术语、产品功能与常见问题。企业知识库优先；内部知识不足时可开启联网检索（联网前自动脱敏，回答附来源链接）。
              </p>
            </div>
          ) : (
            <div className="flex flex-col gap-5">
              {messages.map((m) => (
                <MessageBubble key={m.messageId} message={m} />
              ))}
            </div>
          )}
        </div>

        {/* 输入区 */}
        <div className="border-t border-divider p-4">
          {pendingAttachments.length > 0 && (
            <div className="mb-2.5 flex flex-wrap gap-2">
              {pendingAttachments.map((a, i) => (
                <span
                  key={`${a.name}-${i}`}
                  className="inline-flex items-center gap-2 rounded-capsule bg-pearl px-3 py-1.5 text-[12px] text-ink-80"
                >
                  {a.name}
                  <Badge tone={a.scope === 'personal' ? 'blue' : 'gray'}>
                    {a.scope === 'personal' ? '存入个人知识库' : '仅本次使用'}
                  </Badge>
                  <button
                    type="button"
                    className="press text-ink-48 hover:text-danger"
                    onClick={() =>
                      setPendingAttachments(pendingAttachments.filter((_, j) => j !== i))
                    }
                    aria-label="移除附件"
                  >
                    ✕
                  </button>
                </span>
              ))}
            </div>
          )}

          <div className="flex items-end gap-2.5">
            <div className="relative">
              <button
                type="button"
                onClick={() => setAttachMenuOpen(!attachMenuOpen)}
                className="press focus-ring flex size-10 items-center justify-center rounded-full border border-hairline bg-canvas text-[18px] text-ink-80 hover:bg-pearl"
                aria-label="添加附件"
              >
                +
              </button>
              {attachMenuOpen && (
                <div className="absolute bottom-12 left-0 z-10 w-44 rounded-capsule border border-hairline bg-canvas p-1.5">
                  <button
                    type="button"
                    onClick={() => addAttachment('session')}
                    className="press block w-full rounded-utility px-3 py-2 text-left text-[13px] text-ink hover:bg-pearl"
                  >
                    仅本次使用
                    <span className="block text-[11px] text-ink-48">
                      会话结束后不保留
                    </span>
                  </button>
                  <button
                    type="button"
                    onClick={() => addAttachment('personal')}
                    className="press block w-full rounded-utility px-3 py-2 text-left text-[13px] text-ink hover:bg-pearl"
                  >
                    保存到个人知识库
                    <span className="block text-[11px] text-ink-48">
                      仅自己可见、可检索
                    </span>
                  </button>
                </div>
              )}
            </div>

            <textarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  void send()
                }
              }}
              rows={1}
              placeholder="输入问题，Enter 发送，Shift+Enter 换行"
              className="focus-ring max-h-32 min-h-10 flex-1 resize-none rounded-[20px] border border-hairline bg-canvas px-4 py-2.5 text-[14px] leading-[1.5] text-ink"
            />

            {streaming ? (
              <Button
                variant="danger-ghost"
                onClick={() => activeId && api.stopGeneration(activeId)}
              >
                停止生成
              </Button>
            ) : (
              <Button onClick={() => void send()} disabled={!input.trim() || !activeId}>
                发送
              </Button>
            )}
          </div>

          <div className="mt-2.5 flex items-center justify-between">
            <button
              type="button"
              onClick={() => setWebSearch(!webSearch)}
              className={`press focus-ring inline-flex h-7 items-center gap-1.5 rounded-full border px-3 text-[12px] ${
                webSearch
                  ? 'border-2 border-primary-focus font-semibold text-ink'
                  : 'border-hairline text-ink-48 hover:bg-pearl'
              }`}
            >
              <i
                className={`size-1.5 rounded-full ${webSearch ? 'bg-primary' : 'bg-hairline'}`}
              />
              联网检索{webSearch ? '已开启' : '已关闭'}
            </button>
            <span className="text-[11px] text-ink-48">
              联网前自动移除工单号、客户名称与数据库内容
            </span>
          </div>
        </div>
      </Card>
    </div>
  )
}
