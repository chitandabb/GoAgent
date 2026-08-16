import { useQueries } from '@tanstack/react-query'
import { BriefcaseBusiness, MessageSquarePlus } from 'lucide-react'
import { useNavigate } from 'react-router'
import * as api from '@/shared/api'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Button } from '@/shared/ui/Button'
import { cn } from '@/shared/lib/utils'
import type { LocalWorkspace } from './workspace-store'

export function WorkspaceList({
  workspaces,
  activeId,
  onNavigate,
}: {
  workspaces: LocalWorkspace[]
  activeId?: string
  onNavigate?: () => void
}) {
  const navigate = useNavigate()
  const caseQueries = useQueries({
    queries: workspaces.map((workspace) => ({
      queryKey: ['external-case', workspace.externalCaseId],
      queryFn: () => api.getExternalCase(workspace.externalCaseId),
      retry: false,
    })),
  })

  return (
    <div className="flex h-full min-h-0 flex-col bg-canvas">
      <div className="border-b border-divider p-3">
        <Button
          variant="neutral"
          size="sm"
          className="w-full"
          onClick={() => {
            navigate('/cases')
            onNavigate?.()
          }}
        >
          <MessageSquarePlus />
          新建诊断会话
        </Button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        <p className="px-2 pb-2 pt-1 text-[11px] font-semibold text-ink-48">诊断会话</p>
        {workspaces.length === 0 ? (
          <div className="px-2 py-8 text-center">
            <BriefcaseBusiness className="mx-auto mb-2 size-5 text-ink-48" />
            <p className="text-[12px] leading-[1.6] text-ink-48">还没有工单卷宗</p>
          </div>
        ) : (
          workspaces.map((workspace, index) => {
            const extCase = caseQueries[index]?.data
            return (
              <button
                key={workspace.workspaceId}
                type="button"
                onClick={() => {
                  navigate(`/workbench/${workspace.workspaceId}`)
                  onNavigate?.()
                }}
                className={cn(
                  'press mb-1 block w-full rounded-utility px-3 py-2.5 text-left',
                  workspace.workspaceId === activeId ? 'bg-parchment' : 'hover:bg-pearl',
                )}
              >
                <div className="mb-1 flex items-center gap-2">
                  <span className="size-1.5 shrink-0 rounded-full bg-primary" />
                  <span className="truncate text-[12px] font-semibold text-ink">
                    {extCase?.externalCaseKey ?? '工单会话'}
                  </span>
                </div>
                <p className="line-clamp-2 text-[12px] leading-[1.45] text-ink-80">
                  {extCase?.title ?? '正在读取工单…'}
                </p>
                <p className="mt-1.5 text-[10px] text-ink-48">
                  {workspace.taskIds.length} 次运行 · {fmtDateTime(workspace.updatedAt)}
                </p>
              </button>
            )
          })
        )}

        <div className="mt-3 border-t border-divider px-2 pt-3">
          <button
            type="button"
            onClick={() => {
              navigate('/assistant')
              onNavigate?.()
            }}
            className="press flex w-full items-center justify-between py-2 text-left text-[12px] text-ink hover:bg-pearl"
          >
            <span>知识会话</span>
            <span className="rounded-capsule bg-primary/10 px-2 py-0.5 text-[10px] font-semibold text-primary">
              可用
            </span>
          </button>
        </div>
      </div>

      <div className="border-t border-divider px-4 py-3 text-[10px] leading-[1.55] text-ink-48">
        诊断会话列表仅保存在当前浏览器；任务状态、报告与知识会话始终从服务端读取。
      </div>
    </div>
  )
}
