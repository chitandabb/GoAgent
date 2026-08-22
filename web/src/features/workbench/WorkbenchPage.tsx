import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Files, MessageCircle, PanelLeft, Send, ShieldCheck } from 'lucide-react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import * as api from '@/shared/api'
import { ApiError } from '@/shared/api'
import { Button } from '@/shared/ui/Button'
import { Dialog } from '@/shared/ui/Dialog'
import { EmptyState } from '@/shared/ui/EmptyState'
import { SidePanel } from '@/shared/ui/SidePanel'
import { PageLoading } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'
import { DiagnosisRunBlock } from './DiagnosisRunBlock'
import { DossierPanel } from './DossierPanel'
import { WorkspaceList } from './WorkspaceList'
import {
  getLocalWorkspace,
  getLocalWorkspaces,
  openCaseWorkspace,
  rememberWorkspaceTask,
  type LocalWorkspace,
} from '@/shared/workspace/workspace-store'

export function WorkbenchPage() {
  const { workspaceId } = useParams()
  const [searchParams] = useSearchParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [workspace, setWorkspace] = useState<LocalWorkspace | undefined>(() => workspaceId ? getLocalWorkspace(workspaceId) : undefined)
  const [workspaces, setWorkspaces] = useState(() => getLocalWorkspaces())
  const [requestText, setRequestText] = useState('')
  const [selectedDataSourceIds, setSelectedDataSourceIds] = useState<string[] | null>(null)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [dossierOpen, setDossierOpen] = useState(false)
  const [fingerprintDialogOpen, setFingerprintDialogOpen] = useState(false)
  const retryPrefilled = useRef('')
  const streamRef = useRef<HTMLDivElement>(null)

  const requestedCaseId = searchParams.get('caseId')
  const retryOfTaskId = searchParams.get('retryOf')
  useEffect(() => {
    setRequestText('')
    setSelectedDataSourceIds(null)
    retryPrefilled.current = ''
  }, [workspaceId])

  useEffect(() => {
    if (workspaceId) {
      setWorkspace(getLocalWorkspace(workspaceId))
      setWorkspaces(getLocalWorkspaces())
      return
    }
    if (requestedCaseId) {
      const next = openCaseWorkspace(requestedCaseId)
      navigate(`/workbench/${next.workspaceId}`, { replace: true })
      return
    }
    const first = getLocalWorkspaces()[0]
    if (first) navigate(`/workbench/${first.workspaceId}`, { replace: true })
  }, [navigate, requestedCaseId, workspaceId])

  const extCase = useQuery({
    queryKey: ['external-case', workspace?.externalCaseId],
    queryFn: () => api.getExternalCase(workspace!.externalCaseId),
    enabled: !!workspace?.externalCaseId,
  })
  const dataSources = useQuery({
    queryKey: ['data-sources'],
    queryFn: api.listDataSources,
    enabled: !!workspace,
  })
  const pendingCases = useQuery({
    queryKey: ['external-cases', extCase.data?.dataSourceId, 'workbench-pending'],
    queryFn: () => api.listExternalCases({ dataSourceId: extCase.data!.dataSourceId, status: 'open', page: 1, pageSize: 10 }),
    enabled: !!extCase.data?.dataSourceId,
  })
  const retryOfTask = useQuery({
    queryKey: ['task', retryOfTaskId],
    queryFn: () => api.getTask(retryOfTaskId!),
    enabled: !!retryOfTaskId,
  })

  useEffect(() => {
    if (!retryOfTaskId || !retryOfTask.data || retryPrefilled.current === retryOfTaskId) return
    retryPrefilled.current = retryOfTaskId
    // 重试只恢复仍存在的业务输入（问题文本），不恢复已删除的 Skill/Capability。
    setRequestText(retryOfTask.data.requestText)
  }, [retryOfTask.data, retryOfTaskId])

  useEffect(() => {
    if (extCase.data && selectedDataSourceIds === null) {
      setSelectedDataSourceIds([extCase.data.dataSourceId])
    }
  }, [extCase.data, selectedDataSourceIds])

  const create = useMutation({
    mutationFn: ({ input, idempotencyKey }: {
      input: Parameters<typeof api.createDiagnosisTask>[0]
      idempotencyKey: string
    }) => api.createDiagnosisTask(input, idempotencyKey),
    retry: (failureCount, error) => failureCount < 2 && error instanceof ApiError && error.code === 50301,
    onSuccess: ({ taskId }) => {
      if (!workspace || !extCase.data) return
      const updated = rememberWorkspaceTask(workspace.workspaceId, taskId)
      setWorkspace(updated)
      setWorkspaces(getLocalWorkspaces())
      setRequestText('')
      void queryClient.invalidateQueries({ queryKey: ['case-tasks'] })
      void queryClient.invalidateQueries({ queryKey: ['diagnosis-tasks'] })
      if (retryOfTaskId) navigate(`/workbench/${workspace.workspaceId}`, { replace: true })
      toast.success('排查任务已提交')
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 40923) setFingerprintDialogOpen(true)
    },
  })

  const checkedDataSources = selectedDataSourceIds ?? (extCase.data ? [extCase.data.dataSourceId] : [])
  const chronologicalTaskIds = useMemo(() => [...(workspace?.taskIds ?? [])].reverse(), [workspace?.taskIds])
  useEffect(() => {
    const stream = streamRef.current
    if (!stream || chronologicalTaskIds.length === 0) return
    requestAnimationFrame(() => stream.scrollTo({ top: stream.scrollHeight, behavior: 'smooth' }))
  }, [chronologicalTaskIds.length])

  const toggleDataSource = (dataSourceId: string) => {
    setSelectedDataSourceIds((current) => {
      const selected = current ?? []
      return selected.includes(dataSourceId) ? selected.filter((value) => value !== dataSourceId) : [...selected, dataSourceId]
    })
  }
  const submit = () => {
    if (!workspace || !extCase.data || !requestText.trim() || checkedDataSources.length === 0) return
    create.mutate({
      idempotencyKey: api.createIdempotencyKey(),
      input: {
        externalCaseId: extCase.data.externalCaseId,
        expectedSourceFingerprint: extCase.data.sourceFingerprint,
        evidenceDataSourceIds: checkedDataSources,
        requestText: requestText.trim(),
        retryOfTaskId,
      },
    })
  }
  const openCase = (externalCaseId: string, forceNew = false) => {
    const next = openCaseWorkspace(externalCaseId, { forceNew })
    setDossierOpen(false)
    navigate(`/workbench/${next.workspaceId}`)
  }
  const refreshCase = async () => {
    await queryClient.invalidateQueries({ queryKey: ['external-case', workspace?.externalCaseId] })
    setFingerprintDialogOpen(false)
    toast.success('工单已刷新，请确认信息后重新提交')
  }

  if (!workspaceId && !requestedCaseId && workspaces.length === 0) {
    return (
      <div className="flex h-full items-center justify-center px-6">
        <EmptyState
          title="选择一张工单开始"
          description="选择客户工单后，可将现场信息整理成便于开发跟进的排查单。"
          action={<Button onClick={() => navigate('/cases')}>选择工单</Button>}
        />
      </div>
    )
  }
  if (workspaceId && !workspace) {
    return (
      <div className="flex h-full items-center justify-center px-6">
        <EmptyState
          title="当前浏览器没有这条记录"
          description="请重新选择工单；已经提交的任务仍可在排查任务中查看。"
          action={<Button onClick={() => navigate('/cases')}>重新选择工单</Button>}
        />
      </div>
    )
  }
  if (!workspace || extCase.isPending || dataSources.isPending) return <PageLoading />
  if (extCase.isError || !extCase.data) {
    return (
      <div className="flex h-full items-center justify-center px-6">
        <EmptyState title="工单信息不可读取" description={extCase.error instanceof Error ? extCase.error.message : '记录已失效或当前账号无权访问'} action={<Button onClick={() => navigate('/cases')}>重新选择工单</Button>} />
      </div>
    )
  }

  const dossier = (
    <DossierPanel
      extCase={extCase.data}
      dataSources={dataSources.data ?? []}
      selectedDataSourceIds={checkedDataSources}
      pendingCases={pendingCases.data?.items ?? []}
      onDataSourceToggle={toggleDataSource}
      onOpenCase={openCase}
      onNewWorkspace={() => openCase(extCase.data.externalCaseId, true)}
    />
  )

  return (
    <div className="grid h-full min-h-0 bg-canvas lg:grid-cols-[224px_minmax(0,1fr)] xl:grid-cols-[224px_minmax(0,1fr)_320px]">
      <aside className="hidden min-h-0 border-r border-hairline lg:block">
        <WorkspaceList workspaces={workspaces} activeId={workspace.workspaceId} />
      </aside>

      <section className="flex min-h-0 min-w-0 flex-col bg-parchment">
        <header className="flex h-12 shrink-0 items-center justify-between gap-3 border-b border-hairline bg-canvas px-3 sm:px-5">
          <div className="flex min-w-0 items-center gap-2">
            <Button size="icon" variant="neutral" className="!size-8 lg:hidden" onClick={() => setHistoryOpen(true)} title="打开会话列表">
              <PanelLeft />
              <span className="sr-only">打开会话列表</span>
            </Button>
            <div className="min-w-0">
              <p className="truncate text-[12px] font-semibold text-ink">{extCase.data.externalCaseKey} · {extCase.data.title}</p>
              <p className="text-[10px] text-ink-48">排查记录 · {workspace.taskIds.length} 次处理</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button
              size="icon"
              variant="neutral"
              className="!size-8"
              onClick={() => navigate(`/assistant?caseId=${encodeURIComponent(extCase.data.externalCaseId)}`)}
              title="向助手询问当前工单"
            >
              <MessageCircle />
              <span className="sr-only">向助手询问当前工单</span>
            </Button>
            <Button size="sm" variant="neutral" className="xl:hidden" onClick={() => setDossierOpen(true)}>
              <Files />
              工单信息
            </Button>
          </div>
        </header>

        <div ref={streamRef} className="min-h-0 flex-1 overflow-y-auto px-3 py-6 sm:px-6">
          <div className="mx-auto flex max-w-[760px] flex-col gap-7">
            {chronologicalTaskIds.length === 0 ? (
              <div className="flex min-h-[360px] flex-col items-center justify-center text-center">
                <div className="mb-4 flex size-11 items-center justify-center rounded-utility bg-ink text-white"><ShieldCheck className="size-5" /></div>
                <h1 className="text-[20px] font-semibold text-ink">工单已准备</h1>
                <p className="mt-2 max-w-md text-[13px] leading-[1.7] text-ink-48">描述客户反馈或现场问题，系统会整理成便于开发跟进的排查单。</p>
              </div>
            ) : (
              chronologicalTaskIds.map((taskId) => <DiagnosisRunBlock key={taskId} taskId={taskId} />)
            )}
          </div>
        </div>

        <div className="frosted shrink-0 border-t border-hairline px-3 py-3 sm:px-6">
          <div className="mx-auto max-w-[760px]">
            {create.isError && !(create.error instanceof ApiError && create.error.code === 40923) && <p className="mb-2 text-[11px] text-danger">{create.error instanceof Error ? create.error.message : '排查任务提交失败'}</p>}
            <div className="flex items-end gap-2 rounded-[18px] border border-hairline bg-canvas p-2 pl-4">
              <textarea
                value={requestText}
                onChange={(event) => setRequestText(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' && !event.shiftKey) {
                    event.preventDefault()
                    submit()
                  }
                }}
                rows={1}
                maxLength={20000}
                placeholder="描述客户反馈或现场问题，Enter 生成排查单"
                className="max-h-32 min-h-10 min-w-0 flex-1 resize-none bg-transparent py-2 text-[13px] leading-[1.55] text-ink outline-none placeholder:text-ink-48"
              />
              <Button size="icon" disabled={create.isPending || !requestText.trim() || checkedDataSources.length === 0} onClick={submit} title="生成排查单">
                <Send />
                <span className="sr-only">生成排查单</span>
              </Button>
            </div>
          </div>
        </div>
      </section>

      <aside className="hidden min-h-0 overflow-y-auto border-l border-hairline xl:block">{dossier}</aside>

      <SidePanel open={historyOpen} title="排查记录" side="left" onClose={() => setHistoryOpen(false)}>
        <WorkspaceList workspaces={workspaces} activeId={workspace.workspaceId} onNavigate={() => setHistoryOpen(false)} />
      </SidePanel>
      <SidePanel open={dossierOpen} title="工单信息" side="right" onClose={() => setDossierOpen(false)}>{dossier}</SidePanel>

      <Dialog
        open={fingerprintDialogOpen}
        title="工单内容已变化"
        onClose={() => setFingerprintDialogOpen(false)}
        footer={<><Button variant="neutral" onClick={() => setFingerprintDialogOpen(false)}>稍后处理</Button><Button onClick={() => void refreshCase()}>刷新工单</Button></>}
      >
        <p className="text-[13px] leading-[1.7] text-ink-80">工单内容在你打开后发生了变化。本次未提交，请刷新工单并重新确认。</p>
      </Dialog>
    </div>
  )
}
