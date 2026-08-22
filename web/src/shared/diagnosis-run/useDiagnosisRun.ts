import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as api from '@/shared/api'
import { ApiError } from '@/shared/api'
import type { SseConnectionState, TaskEvent } from '@/shared/api/m1-types'
import { useToast } from '@/shared/ui/Toast'

interface UseDiagnosisRunOptions {
  cancelSuccessMessage?: string
  recoverSuccessMessage?: string
}

function mergeEvents(current: TaskEvent[], incoming: TaskEvent): TaskEvent[] {
  if (current.some((event) => event.seq === incoming.seq)) return current
  return [...current, incoming].sort((a, b) => a.seq - b.seq)
}

export function useDiagnosisRun(taskId: string, options: UseDiagnosisRunOptions = {}) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [events, setEvents] = useState<TaskEvent[]>([])
  const [connection, setConnection] = useState<SseConnectionState>('loading-history')
  const [streamError, setStreamError] = useState('')
  const [cancelOpen, setCancelOpen] = useState(false)
  const [recoverOpen, setRecoverOpen] = useState(false)
  const [recoverReason, setRecoverReason] = useState('')
  const [streamGeneration, setStreamGeneration] = useState(0)

  const task = useQuery({
    queryKey: ['task', taskId],
    queryFn: () => api.getTask(taskId),
    refetchInterval: (query) => {
      const value = query.state.data
      return value && ['pending', 'running', 'cancel_requested'].includes(value.status) ? 5000 : false
    },
  })

  useEffect(() => {
    setEvents([])
    setConnection('loading-history')
    setStreamError('')
    if (!task.isSuccess) return

    return api.subscribeTaskEvents(taskId, {
      onEvent: (event) => {
        setEvents((current) => mergeEvents(current, event))
        void queryClient.invalidateQueries({ queryKey: ['task', taskId] })
      },
      onStatus: (state) => {
        setConnection(state)
        if (state === 'connected') setStreamError('')
      },
      onTerminal: () => void queryClient.invalidateQueries({ queryKey: ['task', taskId] }),
      onError: (error) => setStreamError(error.message),
    })
  }, [queryClient, streamGeneration, task.isSuccess, taskId])

  const cancel = useMutation({
    mutationFn: () => api.cancelTask(taskId),
    onSuccess: (updated) => {
      queryClient.setQueryData(['task', taskId], updated)
      setCancelOpen(false)
      toast.success(options.cancelSuccessMessage ?? '已提交取消请求')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '取消失败'),
  })

  const recover = useMutation({
    mutationFn: ({ reason, idempotencyKey }: { reason: string; idempotencyKey: string }) =>
      api.recoverTask(taskId, reason, idempotencyKey),
    retry: (failureCount, error) => failureCount < 2 && error instanceof ApiError && error.code === 50301,
    onSuccess: () => {
      setRecoverOpen(false)
      setRecoverReason('')
      setStreamGeneration((value) => value + 1)
      toast.success(options.recoverSuccessMessage ?? '任务已重新入队')
      void queryClient.invalidateQueries({ queryKey: ['task', taskId] })
    },
  })

  const reconnect = () => {
    setStreamError('')
    setConnection('loading-history')
    setStreamGeneration((value) => value + 1)
  }

  return {
    task,
    events,
    connection,
    streamError,
    reconnect,
    cancel,
    cancelOpen,
    setCancelOpen,
    recover,
    recoverOpen,
    setRecoverOpen,
    recoverReason,
    setRecoverReason,
  }
}
