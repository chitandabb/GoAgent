import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronLeft, ClipboardCopy, ExternalLink, FileText, ListFilter } from 'lucide-react'
import { Link } from 'react-router'
import * as api from '@/shared/api'
import type { ExternalCase } from '@/shared/api/m1-types'
import { EventTimeline } from '@/shared/diagnosis-run/EventTimeline'
import { useDiagnosisRun } from '@/shared/diagnosis-run/useDiagnosisRun'
import { caseStatusMeta, conclusionMeta, priorityMeta, taskStatusMeta } from '@/shared/lib/status'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { SearchInput, Select } from '@/shared/ui/Field'
import { Spinner } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'

function caseClipboardText(extCase: ExternalCase): string {
  return [
    `工单号：${extCase.externalCaseKey}`,
    `标题：${extCase.title}`,
    `客户：${extCase.customerName || '—'}`,
    `产品：${[extCase.productName, extCase.productVersion].filter(Boolean).join(' ') || '—'}`,
    `优先级：${priorityMeta[extCase.priority].label}`,
    `状态：${caseStatusMeta[extCase.status].label}`,
    `现场描述：${extCase.description}`,
  ].join('\n')
}

async function copyText(text: string): Promise<void> {
  if (!navigator.clipboard?.writeText) throw new Error('当前浏览器不支持剪贴板')
  await navigator.clipboard.writeText(text)
}

function TaskProgress({ taskId }: { taskId: string }) {
  const { task, events, connection } = useDiagnosisRun(taskId)
  const report = useQuery({
    queryKey: ['report', taskId],
    queryFn: () => api.getReportByTask(taskId),
    enabled: task.data?.reportAvailable === true,
    retry: false,
  })

  if (task.isPending) {
    return (
      <div className="flex items-center gap-2 py-5 text-[12px] text-ink-48">
        <Spinner /> 正在读取任务进度
      </div>
    )
  }
  if (task.isError || !task.data) {
    return <p className="py-4 text-[12px] text-danger">任务进度暂时不可读取</p>
  }

  const value = task.data
  const active = ['pending', 'running', 'cancel_requested'].includes(value.status)
  const status = taskStatusMeta[value.status]

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Badge tone={status.tone} dot={active}>{status.label}</Badge>
          <span className="text-[11px] text-ink-48">排查任务</span>
        </div>
        <Button asChild size="icon" variant="neutral" className="!size-8" title="打开任务详情">
          <Link to={`/tasks/${taskId}`}>
            <ExternalLink />
            <span className="sr-only">打开任务详情</span>
          </Link>
        </Button>
      </div>

      <div className="rounded-utility bg-pearl px-3 py-3">
        <p className="text-[11px] font-semibold text-ink">本次要求</p>
        <p className="mt-1 line-clamp-3 text-[11px] leading-[1.6] text-ink-80">{value.requestText}</p>
      </div>

      <div>
        <div className="mb-3 flex items-center justify-between gap-2">
          <h3 className="text-[12px] font-semibold text-ink">处理过程</h3>
          <span className="text-[10px] text-ink-48">
            {active && connection === 'connected' ? '实时更新' : active ? '正在连接' : '已结束'}
          </span>
        </div>
        <EventTimeline events={events} live={active && connection !== 'closed' && connection !== 'failed'} />
      </div>

      {report.data && (
        <div className="rounded-utility border border-ok/20 bg-ok-soft px-3.5 py-3">
          <div className="flex items-center justify-between gap-2">
            <p className="text-[12px] font-semibold text-ink">处理结论</p>
            <Badge tone={conclusionMeta[report.data.conclusionStatus].tone}>
              {conclusionMeta[report.data.conclusionStatus].label}
            </Badge>
          </div>
          <p className="mt-2 line-clamp-4 text-[11px] leading-[1.65] text-ink-80">
            {report.data.businessSummary || report.data.conclusion}
          </p>
          <Link to={`/tasks/${taskId}/report`} className="press mt-2.5 inline-block text-[11px] font-semibold text-primary">
            查看完整排查单 →
          </Link>
        </div>
      )}
    </div>
  )
}

function CaseSummary({ extCase, onCopyKey }: { extCase: ExternalCase; onCopyKey: () => void }) {
  return (
    <>
      <div>
        <div className="flex items-center gap-1.5">
          <p className="text-[11px] font-semibold text-primary">{extCase.externalCaseKey}</p>
          <button
            type="button"
            className="press focus-ring inline-flex size-6 items-center justify-center rounded-full text-ink-48 hover:bg-pearl hover:text-primary"
            onClick={onCopyKey}
            aria-label="复制工单号"
            title="复制工单号"
          >
            <ClipboardCopy className="size-3.5" aria-hidden="true" />
          </button>
        </div>
        <h2 className="mt-1 text-[16px] font-semibold leading-[1.4] text-ink">{extCase.title}</h2>
        <div className="mt-3 flex flex-wrap gap-1.5">
          <Badge tone={caseStatusMeta[extCase.status].tone}>{caseStatusMeta[extCase.status].label}</Badge>
          <Badge tone={priorityMeta[extCase.priority].tone}>优先级 {priorityMeta[extCase.priority].label}</Badge>
        </div>
      </div>
      <div className="border-t border-divider pt-4">
        <div className="mb-2 flex items-center gap-2">
          <FileText className="size-3.5 text-ink-48" />
          <h3 className="text-[12px] font-semibold text-ink">现场描述</h3>
        </div>
        <p className="line-clamp-5 text-[11px] leading-[1.65] text-ink-80">{extCase.description}</p>
      </div>
      <dl className="divide-y divide-divider border-y border-divider text-[11px]">
        <div className="flex justify-between gap-3 py-2.5"><dt className="text-ink-48">客户</dt><dd className="text-right text-ink-80">{extCase.customerName || '—'}</dd></div>
        <div className="flex justify-between gap-3 py-2.5"><dt className="text-ink-48">产品</dt><dd className="text-right text-ink-80">{[extCase.productName, extCase.productVersion].filter(Boolean).join(' ') || '—'}</dd></div>
      </dl>
    </>
  )
}

function CaseIndex({
  selectedCaseId,
  onSelectCase,
}: {
  selectedCaseId?: string
  onSelectCase: (caseId: string) => void
}) {
  const [dataSourceId, setDataSourceId] = useState('')
  const [keyword, setKeyword] = useState('')
  const [queryKeyword, setQueryKeyword] = useState('')
  const [page, setPage] = useState(1)
  const dataSources = useQuery({ queryKey: ['data-sources'], queryFn: api.listDataSources })

  useEffect(() => {
    if (!dataSourceId && dataSources.data?.[0]) setDataSourceId(dataSources.data[0].id)
  }, [dataSourceId, dataSources.data])

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setQueryKeyword(keyword.trim())
      setPage(1)
    }, 250)
    return () => window.clearTimeout(timer)
  }, [keyword])

  const cases = useQuery({
    queryKey: ['external-cases', dataSourceId, 'all', queryKeyword, page, 'dossier'],
    queryFn: () => api.listExternalCases({
      dataSourceId,
      keyword: queryKeyword || undefined,
      page,
      pageSize: 8,
    }),
    enabled: Boolean(dataSourceId),
  })
  const rows = cases.data?.items ?? []
  const totalPages = Math.max(1, Math.ceil((cases.data?.total ?? 0) / 8))

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="space-y-2.5 border-b border-divider px-4 py-4">
        {dataSources.data && dataSources.data.length > 1 && (
          <Select
            value={dataSourceId}
            onValueChange={(value) => {
              setDataSourceId(value)
              setPage(1)
            }}
            aria-label="选择工单数据源"
            className="!h-9 !rounded-capsule !text-[12px]"
          >
            {dataSources.data.map((source) => (
              <option key={source.id} value={source.id}>{source.name}</option>
            ))}
          </Select>
        )}
        <SearchInput
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
          placeholder="搜索工单号、标题或客户"
          aria-label="搜索工单"
          className="[&_input]:!h-9 [&_input]:!text-[12px]"
        />
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-2.5">
        {dataSources.isPending || cases.isPending ? (
          <div className="flex items-center justify-center gap-2 py-10 text-[12px] text-ink-48">
            <Spinner /> 正在读取工单
          </div>
        ) : dataSources.isError || cases.isError ? (
          <div className="px-4 py-10 text-center">
            <p className="text-[12px] font-semibold text-ink">工单暂时不可用</p>
            <Button
              size="sm"
              variant="neutral"
              className="mt-3"
              onClick={() => void (dataSources.isError ? dataSources.refetch() : cases.refetch())}
            >
              重新加载
            </Button>
          </div>
        ) : dataSources.data?.length === 0 ? (
          <p className="px-4 py-10 text-center text-[12px] text-ink-48">没有可用工单数据源</p>
        ) : rows.length === 0 ? (
          <p className="px-4 py-10 text-center text-[12px] text-ink-48">没有符合条件的工单</p>
        ) : (
          <div className="space-y-1.5">
            {rows.map((item) => {
              const selected = item.externalCaseId === selectedCaseId
              return (
                <button
                  key={item.externalCaseId}
                  type="button"
                  onClick={() => onSelectCase(item.externalCaseId)}
                  className={`press focus-ring block w-full rounded-capsule border px-3 py-3 text-left ${
                    selected ? 'border-primary bg-info-soft' : 'border-transparent hover:border-hairline hover:bg-pearl'
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-[11px] font-semibold text-primary">{item.externalCaseKey}</span>
                    <Badge tone={priorityMeta[item.priority].tone}>{priorityMeta[item.priority].label}</Badge>
                  </div>
                  <p className="mt-1 line-clamp-2 text-[12px] font-semibold leading-[1.5] text-ink">{item.title}</p>
                  <p className="mt-1 line-clamp-1 text-[10px] text-ink-48">{item.customerName || '未填写客户'}</p>
                </button>
              )
            })}
          </div>
        )}
      </div>

      {totalPages > 1 && (
        <div className="flex items-center justify-between border-t border-divider px-3 py-2.5 text-[10px] text-ink-48">
          <button
            type="button"
            className="press focus-ring rounded-capsule px-2 py-1 hover:bg-pearl disabled:opacity-35"
            disabled={page <= 1}
            onClick={() => setPage((value) => value - 1)}
          >
            上一页
          </button>
          <span>{page} / {totalPages}</span>
          <button
            type="button"
            className="press focus-ring rounded-capsule px-2 py-1 hover:bg-pearl disabled:opacity-35"
            disabled={page >= totalPages}
            onClick={() => setPage((value) => value + 1)}
          >
            下一页
          </button>
        </div>
      )}
    </div>
  )
}

export function AssistantDossierPanel({
  extCase,
  loading,
  taskId,
  onSelectCase,
  onClearCase,
}: {
  extCase?: ExternalCase
  loading: boolean
  taskId?: string
  onSelectCase: (caseId: string) => void
  onClearCase: () => void
}) {
  const toast = useToast()
  const [showIndex, setShowIndex] = useState(false)

  useEffect(() => {
    if (extCase) setShowIndex(false)
  }, [extCase?.externalCaseId])

  const copyCaseKey = async () => {
    if (!extCase) return
    try {
      await copyText(extCase.externalCaseKey)
      toast.success('工单号已复制')
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '复制失败')
    }
  }

  const copyCaseSummary = async () => {
    if (!extCase) return
    try {
      await copyText(caseClipboardText(extCase))
      toast.success('工单摘要已复制')
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '复制失败')
    }
  }

  const indexVisible = showIndex || (!extCase && !loading)

  return (
    <aside className="hidden w-[320px] shrink-0 flex-col overflow-hidden rounded-card border border-hairline bg-canvas xl:flex">
      <div className="flex min-h-[53px] items-center justify-between gap-2 border-b border-divider px-4 py-3">
        <div className="flex min-w-0 items-center gap-2">
          {indexVisible && extCase && (
            <button
              type="button"
              className="press focus-ring inline-flex size-7 shrink-0 items-center justify-center rounded-full text-ink-48 hover:bg-pearl hover:text-ink"
              onClick={() => setShowIndex(false)}
              aria-label="返回当前卷宗"
              title="返回当前卷宗"
            >
              <ChevronLeft className="size-4" aria-hidden="true" />
            </button>
          )}
          <p className="truncate text-[13px] font-semibold text-ink">{indexVisible ? '选择工单' : '工单卷宗'}</p>
        </div>
        {!indexVisible && extCase && (
          <div className="flex items-center gap-1">
            <button
              type="button"
              className="press focus-ring inline-flex size-8 items-center justify-center rounded-full text-ink-48 hover:bg-pearl hover:text-primary"
              onClick={() => setShowIndex(true)}
              aria-label="切换工单"
              title="切换工单"
            >
              <ListFilter className="size-4" aria-hidden="true" />
            </button>
            <button
              type="button"
              className="press focus-ring inline-flex size-8 items-center justify-center rounded-full text-ink-48 hover:bg-pearl hover:text-primary"
              onClick={() => void copyCaseSummary()}
              aria-label="复制工单摘要"
              title="复制工单摘要"
            >
              <ClipboardCopy className="size-4" aria-hidden="true" />
            </button>
          </div>
        )}
      </div>
      {indexVisible ? (
        <CaseIndex
          selectedCaseId={extCase?.externalCaseId}
          onSelectCase={(caseId) => {
            onSelectCase(caseId)
            setShowIndex(false)
          }}
        />
      ) : (
        <div className="flex-1 overflow-y-auto px-5 py-5">
          {loading ? (
          <div className="flex items-center gap-2 py-8 text-[12px] text-ink-48"><Spinner /> 正在读取工单</div>
        ) : extCase ? (
          <div className="flex flex-col gap-5">
            <CaseSummary extCase={extCase} onCopyKey={() => void copyCaseKey()} />
            <div className="flex gap-2">
              <Button asChild size="sm" variant="neutral" className="flex-1">
                <Link to={`/cases/${extCase.externalCaseId}`}>
                  <ExternalLink /> 打开工单
                </Link>
              </Button>
              <Button size="sm" variant="neutral" className="flex-1" onClick={onClearCase}>
                取消关联
              </Button>
            </div>
            <section className="border-t border-divider pt-5">
              {taskId ? (
                <TaskProgress taskId={taskId} />
              ) : (
                <div className="rounded-utility border border-dashed border-hairline px-4 py-5 text-center">
                  <p className="text-[12px] font-semibold text-ink">尚未开始排查</p>
                  <p className="mt-1 text-[11px] leading-[1.6] text-ink-48">在对话中说明需要核对的问题，助手会在这里更新处理进度。</p>
                </div>
              )}
            </section>
          </div>
        ) : (
          <div />
        )}
        </div>
      )}
    </aside>
  )
}
