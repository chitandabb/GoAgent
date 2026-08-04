import { listTaskEvents } from './business'
import { ApiError } from './errors'
import type { SseConnectionState, TaskEvent, TaskEventType } from './m1-types'

const maxReconnectAttempts = 5
const baseReconnectDelayMs = 1000
const maxReconnectDelayMs = 10_000

const eventTypes: TaskEventType[] = [
  'task_created',
  'task_cancel_requested',
  'task_started',
  'task_reclaimed',
  'task_retry_scheduled',
  'task_succeeded',
  'task_failed',
  'task_cancelled',
  'task_requeued',
]

const terminalEvents = new Set<TaskEventType>([
  'task_succeeded',
  'task_failed',
  'task_cancelled',
])

interface SubscribeOptions {
  onEvent: (event: TaskEvent) => void
  onStatus: (state: SseConnectionState) => void
  onTerminal: () => void
  onError: (error: Error) => void
}

export function subscribeTaskEvents(
  taskId: string,
  options: SubscribeOptions,
): () => void {
  let active = true
  let source: EventSource | null = null
  let cursor = 0
  let ended = false
  let reconnectTimer: number | null = null
  let reconnectAttempts = 0
  let latestEventType = ''

  const closeSource = () => {
    source?.close()
    source = null
  }

  const fail = (error: Error) => {
    closeSource()
    options.onError(error)
    options.onStatus('failed')
  }

  const emit = (event: TaskEvent, closeOnTerminal = true) => {
    cursor = Math.max(cursor, event.seq)
    latestEventType = event.eventType
    options.onEvent(event)
    if (closeOnTerminal && terminalEvents.has(event.eventType as TaskEventType)) {
      ended = true
      closeSource()
      options.onStatus('closed')
      options.onTerminal()
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
      `/api/v1/diagnosis-tasks/${taskId}/events?afterSeq=${cursor}&limit=100`,
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
    eventTypes.forEach((type) => {
      nextSource.addEventListener(type, (message) => {
        if (!active) return
        try {
          emit(JSON.parse((message as MessageEvent<string>).data) as TaskEvent)
        } catch {
          options.onError(new Error('收到无法解析的任务事件'))
        }
      })
    })
  }

  async function loadHistory(initial: boolean) {
    options.onStatus(initial ? 'loading-history' : 'reconnecting')
    try {
      let hasMore = true
      while (active && hasMore) {
        const page = await listTaskEvents(taskId, cursor)
        page.items.forEach((event) => emit(event, false))
        cursor = Math.max(cursor, page.nextAfterSeq)
        hasMore = page.hasMore
      }
      if (!active) return
      if (terminalEvents.has(latestEventType as TaskEventType)) {
        ended = true
        options.onStatus('closed')
        options.onTerminal()
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
