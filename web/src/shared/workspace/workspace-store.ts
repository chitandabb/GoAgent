// 浏览器侧的工作区索引只负责导航上下文，不替代服务端任务事实。
export interface LocalWorkspace {
  workspaceId: string
  externalCaseId: string
  taskIds: string[]
  createdAt: string
  updatedAt: string
}

// v2 invalidates pre-cleanup task references that only existed in the browser session.
const STORAGE_KEY = 'mesguard.local-workspaces.v2'
const MAX_WORKSPACES = 12
const MAX_TASKS_PER_WORKSPACE = 20

export function getLocalWorkspaces(): LocalWorkspace[] {
  try {
    const value = JSON.parse(sessionStorage.getItem(STORAGE_KEY) ?? '[]')
    if (!Array.isArray(value)) return []
    return value.filter(
      (item): item is LocalWorkspace =>
        typeof item?.workspaceId === 'string' &&
        typeof item?.externalCaseId === 'string' &&
        Array.isArray(item?.taskIds) &&
        item.taskIds.every((taskId: unknown) => typeof taskId === 'string') &&
        typeof item?.createdAt === 'string' &&
        typeof item?.updatedAt === 'string',
    )
  } catch {
    return []
  }
}

function saveLocalWorkspaces(workspaces: LocalWorkspace[]): void {
  sessionStorage.setItem(STORAGE_KEY, JSON.stringify(workspaces.slice(0, MAX_WORKSPACES)))
}

export function getLocalWorkspace(workspaceId: string): LocalWorkspace | undefined {
  return getLocalWorkspaces().find((workspace) => workspace.workspaceId === workspaceId)
}

export function openCaseWorkspace(
  externalCaseId: string,
  options: { forceNew?: boolean } = {},
): LocalWorkspace {
  const workspaces = getLocalWorkspaces()
  if (!options.forceNew) {
    const existing = workspaces.find((workspace) => workspace.externalCaseId === externalCaseId)
    if (existing) return existing
  }

  const now = new Date().toISOString()
  const workspace: LocalWorkspace = {
    workspaceId: crypto.randomUUID(),
    externalCaseId,
    taskIds: [],
    createdAt: now,
    updatedAt: now,
  }
  saveLocalWorkspaces([workspace, ...workspaces])
  return workspace
}

export function rememberWorkspaceTask(workspaceId: string, taskId: string): LocalWorkspace | undefined {
  const workspaces = getLocalWorkspaces()
  const current = workspaces.find((workspace) => workspace.workspaceId === workspaceId)
  if (!current) return undefined

  const updated: LocalWorkspace = {
    ...current,
    taskIds: [taskId, ...current.taskIds.filter((value) => value !== taskId)].slice(
      0,
      MAX_TASKS_PER_WORKSPACE,
    ),
    updatedAt: new Date().toISOString(),
  }
  saveLocalWorkspaces([
    updated,
    ...workspaces.filter((workspace) => workspace.workspaceId !== workspaceId),
  ])
  return updated
}

export function findWorkspaceForTask(taskId: string): LocalWorkspace | undefined {
  return getLocalWorkspaces().find((workspace) => workspace.taskIds.includes(taskId))
}
