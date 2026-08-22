export type AssistantProgressPhase =
  | 'submitting'
  | 'queued'
  | 'running'
  | 'retry'
  | 'reconnecting'
  | 'finalizing'
  | 'failed'

export interface AssistantProgressActivity {
  displayName: string
  status: 'running' | 'succeeded' | 'failed'
}

export interface AssistantProgressState {
  phase: AssistantProgressPhase
  activities: readonly AssistantProgressActivity[]
  hasOutput: boolean
}

export function assistantProgressLabel(state: AssistantProgressState): string {
  if (state.phase === 'retry') return '回答校验未通过，正在自动重试'
  if (state.phase === 'running') {
    if (state.hasOutput) return '正在生成回答'
    const runningActivity = [...state.activities].reverse().find((activity) => activity.status === 'running')
    if (runningActivity) {
      return runningActivity.displayName.startsWith('正在')
        ? runningActivity.displayName
        : `正在${runningActivity.displayName}`
    }
    if (!state.hasOutput && state.activities.length > 0 && state.activities.every((activity) => activity.status !== 'running')) {
      return '资料已返回，正在组织回答'
    }
    return '正在理解问题并选择信息来源'
  }
  switch (state.phase) {
    case 'submitting':
      return '正在提交问题'
    case 'queued':
      return '问题已收到，正在等待处理'
    case 'reconnecting':
      return '连接波动，正在恢复处理进度'
    case 'finalizing':
      return '回答已生成，正在整理引用'
    case 'failed':
      return '处理未完成'
  }
}
