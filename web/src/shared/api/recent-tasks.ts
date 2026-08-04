export interface RecentTaskEntry {
  taskId: string
  externalCaseId: string
  createdAt: string
}

const STORAGE_KEY = 'mesguard.recent-diagnosis-tasks.v1'
const MAX_ENTRIES = 20

export function getRecentTasks(): RecentTaskEntry[] {
  try {
    const value = JSON.parse(sessionStorage.getItem(STORAGE_KEY) ?? '[]')
    if (!Array.isArray(value)) return []
    return value.filter(
      (item): item is RecentTaskEntry =>
        typeof item?.taskId === 'string' &&
        typeof item?.externalCaseId === 'string' &&
        typeof item?.createdAt === 'string',
    )
  } catch {
    return []
  }
}

export function rememberRecentTask(entry: RecentTaskEntry): void {
  const next = [entry, ...getRecentTasks().filter((item) => item.taskId !== entry.taskId)]
  sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next.slice(0, MAX_ENTRIES)))
}
