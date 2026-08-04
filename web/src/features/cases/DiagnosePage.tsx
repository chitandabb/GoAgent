import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as api from '@/shared/api'
import { ApiError } from '@/shared/api'
import type {
  InvestigationCapability,
  RequestedSkill,
} from '@/shared/api/m1-types'
import { shortId } from '@/shared/lib/fmt'
import { Button } from '@/shared/ui/Button'
import { Card, CardTitle } from '@/shared/ui/Card'
import { Dialog } from '@/shared/ui/Dialog'
import { FieldLabel, Select, TextArea } from '@/shared/ui/Field'
import { PageHeader } from '@/shared/ui/PageHeader'
import { PageLoading } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'

export function DiagnosePage() {
  const { caseId = '' } = useParams()
  const [searchParams] = useSearchParams()
  const retryOfTaskId = searchParams.get('retryOf')
  const navigate = useNavigate()
  const qc = useQueryClient()
  const toast = useToast()

  const extCase = useQuery({
    queryKey: ['external-case', caseId],
    queryFn: () => api.getExternalCase(caseId),
  })
  const dataSources = useQuery({
    queryKey: ['data-sources'],
    queryFn: api.listDataSources,
  })
  const retryOfTask = useQuery({
    queryKey: ['task', retryOfTaskId],
    queryFn: () => api.getTask(retryOfTaskId!),
    enabled: !!retryOfTaskId,
  })

  const [selectedDs, setSelectedDs] = useState<string[] | null>(null)
  const [requestText, setRequestText] = useState('')
  const [requestedSkill, setRequestedSkill] = useState<RequestedSkill>('ticket-diagnosis')
  const [capabilities, setCapabilities] = useState<InvestigationCapability[]>(['case'])
  const [fpDialogOpen, setFpDialogOpen] = useState(false)

  // 重新诊断：用原任务输入预填一次（仍创建全新任务与快照）
  const prefilled = useRef(false)
  useEffect(() => {
    if (prefilled.current || !retryOfTask.data) return
    prefilled.current = true
    setRequestText(retryOfTask.data.requestText)
    const scope = retryOfTask.data.requestScope
    setRequestedSkill(scope.requestedSkill ?? 'ticket-diagnosis')
    setCapabilities(scope.allowedCapabilities ?? ['case'])
  }, [retryOfTask.data])

  const create = useMutation({
    mutationFn: ({ input, idempotencyKey }: {
      input: Parameters<typeof api.createDiagnosisTask>[0]
      idempotencyKey: string
    }) => api.createDiagnosisTask(input, idempotencyKey),
    retry: (failureCount, error) =>
      failureCount < 2 && error instanceof ApiError && error.code === 50301,
    onSuccess: ({ taskId, createdAt }) => {
      api.rememberRecentTask({ taskId, externalCaseId: caseId, createdAt })
      navigate(`/tasks/${taskId}`, { replace: true })
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === 40923) {
        setFpDialogOpen(true)
      }
    },
  })

  if (extCase.isPending || dataSources.isPending) return <PageLoading />
  if (!extCase.data) {
    return <p className="py-24 text-center text-ink-48">工单不存在或无权访问</p>
  }
  const c = extCase.data
  const dsList = dataSources.data ?? []
  // 默认勾选工单所属数据源（对应“系统推荐数据源，用户确认或修正”）
  const checked = selectedDs ?? [c.dataSourceId]

  const toggleDs = (id: string) => {
    setSelectedDs(
      checked.includes(id) ? checked.filter((x) => x !== id) : [...checked, id],
    )
  }

  const selectSkill = (skill: RequestedSkill) => {
    setRequestedSkill(skill)
    setCapabilities((current) => {
      const required = skill === 'code-investigation' ? 'code' : skill === 'sql-investigation' ? 'sql' : null
      return required && !current.includes(required) ? [...current, required] : current
    })
  }

  const toggleCapability = (capability: 'code' | 'sql') => {
    const required =
      (requestedSkill === 'code-investigation' && capability === 'code') ||
      (requestedSkill === 'sql-investigation' && capability === 'sql')
    if (required) return
    setCapabilities((current) =>
      current.includes(capability)
        ? current.filter((item) => item !== capability)
        : [...current, capability],
    )
  }

  const submit = () => {
    create.mutate({
      idempotencyKey: api.createIdempotencyKey(),
      input: {
        externalCaseId: c.externalCaseId,
        expectedSourceFingerprint: c.sourceFingerprint,
        evidenceDataSourceIds: checked,
        requestText: requestText.trim(),
        requestScope: {
          requestedSkill,
          allowedCapabilities: capabilities,
        },
        requestScopeSchemaVersion: 1,
        attachments: [],
        retryOfTaskId,
      },
    })
  }

  const refreshCase = async () => {
    await qc.invalidateQueries({ queryKey: ['external-case', caseId] })
    setFpDialogOpen(false)
    toast.success('工单已刷新，请确认最新内容后重新提交')
  }

  return (
    <div className="mx-auto max-w-[760px]">
      <Link
        to={`/cases/${c.externalCaseId}`}
        className="press mb-4 inline-block text-[13px] text-primary"
      >
        ‹ 返回工单详情
      </Link>
      <PageHeader
        eyebrow={c.externalCaseKey}
        title="发起诊断"
        subtitle="提交后创建不可变工单快照与异步任务；关闭页面不会中断诊断"
      />

      {retryOfTaskId && (
        <div className="mb-5 flex items-center gap-2.5 rounded-card border border-hairline bg-info-soft px-5 py-3.5">
          <span className="text-[13px] text-ink-80">
            重试自任务
            <Link
              to={`/tasks/${retryOfTaskId}`}
              className="press mx-1 text-primary hover:underline"
            >
              {shortId(retryOfTaskId)}
            </Link>
            —— 将创建全新任务与快照，不覆盖原记录；已预填原任务的输入，可修改。
          </span>
        </div>
      )}

      <Card className="p-7">
        <div className="mb-7">
          <FieldLabel hint="决定首选调查流程">调查入口</FieldLabel>
          <Select value={requestedSkill} onValueChange={(value) => selectSkill(value as RequestedSkill)}>
            <option value="ticket-diagnosis">工单诊断</option>
            <option value="code-investigation">代码调查</option>
            <option value="sql-investigation">SQL 调查</option>
          </Select>
        </div>

        <div className="mb-7">
          <FieldLabel hint="任务允许使用的能力">调查范围</FieldLabel>
          <div className="grid gap-2 sm:grid-cols-3">
            {([
              ['case', '工单调查', '始终启用'],
              ['code', '代码调查', '读取代码证据'],
              ['sql', 'SQL 调查', '执行受控只读查询'],
            ] as const).map(([value, label, note]) => {
              const selected = capabilities.includes(value)
              const locked = value === 'case' ||
                (value === 'code' && requestedSkill === 'code-investigation') ||
                (value === 'sql' && requestedSkill === 'sql-investigation')
              return (
                <label key={value} className="flex min-h-16 items-start gap-3 rounded-utility border border-hairline bg-canvas px-4 py-3">
                  <input
                    type="checkbox"
                    checked={selected}
                    disabled={locked}
                    onChange={() => value !== 'case' && toggleCapability(value)}
                    className="mt-0.5 size-4 accent-primary"
                  />
                  <span>
                    <span className="block text-[13px] font-semibold text-ink">{label}</span>
                    <span className="block text-[11px] text-ink-48">{note}</span>
                  </span>
                </label>
              )
            })}
          </div>
          <p className="mt-2 text-[12px] leading-[1.6] text-ink-48">
            这里表示任务被允许使用的能力，不代表代码服务或 SQL 服务当前在线；系统不会根据在线状态扩大范围。
          </p>
        </div>

        <div className="mb-7">
          <CardTitle className="mb-1">证据数据源</CardTitle>
          <p className="mb-3 text-[12px] text-ink-48">
            系统根据工单推荐，可修正。所有数据库访问均为只读并受白名单约束。
          </p>
          <div className="flex flex-wrap gap-2">
            {dsList.map((d) => {
              const on = checked.includes(d.id)
              return (
                <button
                  key={d.id}
                  type="button"
                  onClick={() => toggleDs(d.id)}
                  className={`press focus-ring h-9 rounded-full border px-4 text-[13px] ${
                    on
                      ? 'border-2 border-primary-focus bg-canvas font-semibold text-ink'
                      : 'border-hairline bg-canvas text-ink-80 hover:bg-pearl'
                  }`}
                >
                  {d.name}
                </button>
              )
            })}
          </div>
        </div>

        <div className="mb-7">
          <FieldLabel hint="自然语言要求不会绕过工具权限">补充说明</FieldLabel>
          <TextArea
            placeholder="例如：引发这个问题的原因是什么？请先检查数据库中的业务状态。"
            value={requestText}
            maxLength={20000}
            onChange={(e) => setRequestText(e.target.value)}
          />
        </div>

        {create.isError && !(create.error instanceof ApiError && create.error.code === 40923) && (
          <p className="mb-4 text-[13px] text-danger">
            {create.error instanceof Error ? create.error.message : '创建失败，请重试'}
          </p>
        )}

        <div className="flex items-center justify-end gap-3 border-t border-divider pt-5">
          <Button variant="neutral" onClick={() => navigate(-1)}>
            取消
          </Button>
          <Button
            onClick={submit}
            disabled={
              create.isPending ||
              checked.length === 0 ||
              !requestText.trim() ||
              requestText.length > 20000
            }
          >
            {create.isPending ? '创建中…' : '创建诊断任务'}
          </Button>
        </div>
      </Card>

      {/* 40923：确认后外部工单发生变化 */}
      <Dialog
        open={fpDialogOpen}
        title="工单内容已变化"
        onClose={() => setFpDialogOpen(false)}
        footer={
          <>
            <Button variant="neutral" onClick={() => setFpDialogOpen(false)}>
              稍后处理
            </Button>
            <Button onClick={() => void refreshCase()}>刷新工单并重新确认</Button>
          </>
        }
      >
        <p className="text-[14px] leading-[1.7] text-ink-80">
          你确认之后，外部系统中的这张工单发生了变化（来源指纹不一致）。为保证诊断输入与你看到的内容一致，系统没有创建任务。
        </p>
        <p className="mt-3 text-[13px] leading-[1.7] text-ink-48">
          刷新后请重新核对工单信息，再提交诊断。
        </p>
      </Dialog>
    </div>
  )
}
