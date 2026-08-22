import { listTurnEvents } from './business'
import { ApiError } from './errors'
import type { SseConnectionState, TurnEvent, TurnEventType } from './m1-types'

const maxReconnectAttempts = 5
const baseReconnectDelayMs = 1000
const maxReconnectDelayMs = 10_000

const turnEventTypes: TurnEventType[] = [
  'turn_queued',
  'turn_running',
  'turn_retry_scheduled',
  'turn_tool_started',
  'turn_tool_completed',
  'turn_message_delta',
  'turn_completed',
  'turn_failed',
]

const terminalTurnEvents = new Set<TurnEventType>(['turn_completed', 'turn_failed'])

export interface SubscribeTurnOptions {
  onEvent: (event: TurnEvent) => void
  onStatus: (state: SseConnectionState) => void
  onTerminal: (finalEvent: TurnEvent | null) => void
  onError: (error: Error) => void
}

/**
 * 订阅会话 turn 的实时事件流：先回放历史（JSON 分页），再建立 EventSource
 * 持续订阅增量；连接中断时按指数退避自动重连并续传。
 */
export function subscribeTurnEvents(
  conversationId: string,
  turnId: string,
  options: SubscribeTurnOptions,
): () => void {
  let active = true
  let source: EventSource | null = null
  let cursor = 0
  let ended = false
  let reconnectTimer: number | null = null
  let reconnectAttempts = 0
  let latestEvent: TurnEvent | null = null

  const closeSource = () => {
    source?.close()
    source = null
  }

  const fail = (error: Error) => {
    closeSource()
    options.onError(error)
    options.onStatus('failed')
  }

  const emit = (event: TurnEvent, closeOnTerminal = true) => {
    cursor = Math.max(cursor, event.seq)
    latestEvent = event
    options.onEvent(event)
    if (closeOnTerminal && terminalTurnEvents.has(event.eventType as TurnEventType)) {
      ended = true
      closeSource()
      options.onStatus('closed')
      options.onTerminal(event)
    }
  }

  const scheduleReconnect = (error: Error) => {
    if (!active || ended || reconnectTimer !== null) return
    if (reconnectAttempts >= maxReconnectAttempts) {
      fail(new Error(`${error.message}，自动重连已停止`))
      return
    }
    const delay = Math.min(
      baseReconnectDelayMs * 2 ** reconnectAttempts,
      maxReconnectDelayMs,
    )
    reconnectAttempts += 1
    options.onError(error)
    options.onStatus('reconnecting')
    reconnectTimer = window.setTimeout(() => {
      reconnectTimer = null
      void loadHistory(false)
    }, delay)
  }

  const connect = () => {
    if (!active || source) return
    const nextSource = new EventSource(
      `/api/v1/conversations/${conversationId}/turns/${turnId}/events?afterSeq=${cursor}&limit=100`,
      { withCredentials: true },
    )
    source = nextSource
    nextSource.onopen = () => {
      if (!active || source !== nextSource) return
      reconnectAttempts = 0
      options.onStatus('connected')
    }
    nextSource.onerror = () => {
      if (!active || source !== nextSource) return
      closeSource()
      scheduleReconnect(new Error('实时事件连接中断'))
    }
    turnEventTypes.forEach((type) => {
      nextSource.addEventListener(type, (message) => {
        if (!active) return
        try {
          emit(JSON.parse((message as MessageEvent<string>).data) as TurnEvent)
        } catch {
          options.onError(new Error('收到无法解析的会话事件'))
        }
      })
    })
  }

  async function loadHistory(initial: boolean) {
    options.onStatus(initial ? 'loading-history' : 'reconnecting')
    try {
      let hasMore = true
      while (active && hasMore) {
        const page = await listTurnEvents(conversationId, turnId, cursor)
        page.items.forEach((event) => emit(event, false))
        cursor = Math.max(cursor, page.nextAfterSeq)
        hasMore = page.hasMore
      }
      if (!active) return
      if (latestEvent && terminalTurnEvents.has(latestEvent.eventType as TurnEventType)) {
        ended = true
        options.onStatus('closed')
        options.onTerminal(latestEvent)
        return
      }
      if (!ended && !source) connect()
    } catch (error) {
      if (!active) return
      if (error instanceof ApiError && error.status === 401) {
        fail(new Error('登录状态已过期，请重新登录'))
        return
      }
      scheduleReconnect(error instanceof Error ? error : new Error('事件加载失败'))
    }
  }

  void loadHistory(true)
  return () => {
    active = false
    if (reconnectTimer !== null) window.clearTimeout(reconnectTimer)
    closeSource()
  }
}
