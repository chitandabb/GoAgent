import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type {
  DiagnosisReportEvidence,
  ReviewVerdict,
} from '@/shared/api/m1-types'
import { conclusionMeta, riskMeta, verdictMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card, CardTitle } from '@/shared/ui/Card'
import { EmptyState } from '@/shared/ui/EmptyState'
import { FieldLabel, TextArea } from '@/shared/ui/Field'
import { PageLoading } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'
import { Wordmark } from '@/shared/ui/Wordmark'

const verdictOptions: { value: ReviewVerdict; label: string }[] = [
  { value: 'adopted', label: '采纳' },
  { value: 'partially_adopted', label: '部分采纳' },
  { value: 'rejected', label: '驳回' },
]

const evidenceSourceLabels: Record<DiagnosisReportEvidence['sourceType'], string> = {
  case_snapshot: '工单快照',
  schema_catalog: 'Schema Catalog',
  sql_object_definition: 'SQL 对象定义',
  sql_query: 'SQL 查询',
  code_search: '代码检索',
  attachment: '附件',
  knowledge_chunk: '知识片段',
  web: '网页',
}

const confidenceLabels = { high: '高', medium: '中', low: '低' } as const
const supportLabels = { supports: '支持', contradicts: '反驳', context: '背景' } as const

function ListSection({ title, items, emptyText }: { title: string; items: string[]; emptyText: string }) {
  return (
    <Card className="p-6">
      <CardTitle className="mb-3">{title}</CardTitle>
      {items.length > 0 ? (
        <ul className="flex list-disc flex-col gap-2 pl-5 text-[13px] leading-[1.65] text-ink-80">
          {items.map((item, index) => <li key={`${item}-${index}`}>{item}</li>)}
        </ul>
      ) : (
        <p className="text-[13px] text-ink-48">{emptyText}</p>
      )}
    </Card>
  )
}

function MetaRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 border-b border-divider py-2.5 last:border-0">
      <dt className="shrink-0 text-[12px] text-ink-48">{label}</dt>
      <dd className="min-w-0 break-words text-right text-[12px] text-ink-80">{children}</dd>
    </div>
  )
}

export function ReportPage() {
  const { taskId = '' } = useParams()
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [verdict, setVerdict] = useState<ReviewVerdict | null>(null)
  const [comment, setComment] = useState('')

  const report = useQuery({
    queryKey: ['report', taskId],
    queryFn: () => api.getReportByTask(taskId),
  })
  const reviews = useQuery({
    queryKey: ['report-reviews', report.data?.reportId],
    queryFn: () => api.listReportReviews(report.data!.reportId),
    enabled: !!report.data?.reportId,
  })
  const submit = useMutation({
    mutationFn: () => api.submitReview(report.data!.reportId, verdict!, comment),
    onSuccess: async () => {
      setVerdict(null)
      setComment('')
      await queryClient.invalidateQueries({ queryKey: ['report-reviews', report.data!.reportId] })
      toast.success('复核已提交，历史记录已从服务端刷新')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '复核提交失败'),
  })

  if (report.isPending) return <PageLoading />
  if (report.isError || !report.data) {
    return (
      <EmptyState
        title="正式报告不可读取"
        description={report.error instanceof Error ? report.error.message : '任务可能尚未成功或当前账号无权访问'}
        action={<Button variant="neutral" onClick={() => void report.refetch()}>重新加载</Button>}
      />
    )
  }

  const value = report.data
  const conclusion = conclusionMeta[value.conclusionStatus]
  const risk = riskMeta[value.riskLevel]
  const history = reviews.data?.items ?? []
  const currentReview = reviews.data?.current

  return (
    <div className="mx-auto max-w-[960px]">
      <div className="mb-4 flex items-center justify-between print:hidden">
        <Link to={`/tasks/${taskId}`} className="press text-[13px] text-primary">‹ 返回任务详情</Link>
        <Button variant="neutral" size="sm" onClick={() => window.print()}>打印 / 导出 PDF</Button>
      </div>

      <div className="mb-6 hidden items-baseline justify-between border-b border-hairline pb-4 print:flex">
        <Wordmark className="text-[18px]" />
        <span className="text-[12px] text-ink-48">报告 {value.reportId} · {fmtDateTime(value.generatedAt)}</span>
      </div>

      <header className="mb-8">
        <p className="mb-2 text-[13px] font-semibold text-ink-48">正式诊断报告</p>
        <Card className="p-7 sm:p-8">
          <div className="flex flex-wrap items-start justify-between gap-5">
            <div>
              <p className="text-[12px] font-semibold text-ink-48">结论状态</p>
              <h1 className="mt-1 text-[28px] font-semibold leading-tight text-ink">{conclusion.label}</h1>
              <p className="mt-2 text-[13px] text-ink-48">置信度 {confidenceLabels[value.confidence]} · {value.partial ? '部分报告' : '完整报告'}</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone={risk.tone}>{risk.label}</Badge>
              {currentReview && <Badge tone={verdictMeta[currentReview.verdict].tone}>{verdictMeta[currentReview.verdict].label}</Badge>}
            </div>
          </div>
          <div className="mt-6 border-t border-divider pt-6">
            <p className="mb-2 text-[12px] font-semibold text-ink-48">结论</p>
            <p className="reading whitespace-pre-wrap">{value.conclusion}</p>
          </div>
        </Card>
      </header>

      <section className="mb-8 grid gap-5 md:grid-cols-2">
        <Card className="p-6">
          <CardTitle className="mb-3">业务摘要</CardTitle>
          <p className="whitespace-pre-wrap text-[14px] leading-[1.75] text-ink-80">{value.businessSummary}</p>
        </Card>
        <Card className="p-6">
          <CardTitle className="mb-3">技术摘要</CardTitle>
          <p className="whitespace-pre-wrap text-[14px] leading-[1.75] text-ink-80">{value.technicalSummary}</p>
        </Card>
        <ListSection title="限制条件" items={value.limitations} emptyText="报告未声明额外限制" />
        <ListSection title="缺失证据" items={value.missingEvidence} emptyText="报告未声明缺失证据" />
      </section>

      <section className="mb-8">
        <h2 className="mb-1 text-[19px] font-semibold text-ink">有序证据声明</h2>
        <p className="mb-4 text-[13px] leading-[1.6] text-ink-48">这里只展示后端返回的证据声明与溯源元数据，不包含也不补造原始证据正文。</p>
        {value.evidence.length === 0 ? (
          <Card><EmptyState title="没有证据声明" description="正式报告未返回证据声明。" /></Card>
        ) : (
          <ol className="flex flex-col gap-3">
            {value.evidence.map((evidence, index) => (
              <li key={`${evidence.evidenceId}-${evidence.claimKey}-${index}`}>
                <Card className="p-5">
                  <div className="flex items-start gap-3.5">
                    <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-info-soft text-[12px] font-semibold text-primary">{index + 1}</span>
                    <div className="min-w-0 flex-1">
                      <div className="mb-2 flex flex-wrap items-center gap-2">
                        <Badge tone="blue">{evidenceSourceLabels[evidence.sourceType]}</Badge>
                        <Badge tone={evidence.supportType === 'contradicts' ? 'red' : evidence.supportType === 'supports' ? 'green' : 'gray'}>{supportLabels[evidence.supportType]}</Badge>
                        <Badge tone={evidence.validityStatus === 'valid' ? 'green' : 'orange'}>{evidence.validityStatus}</Badge>
                      </div>
                      <p className="text-[14px] font-semibold leading-[1.6] text-ink">{evidence.claim}</p>
                      <dl className="mt-3 grid gap-x-5 gap-y-1 text-[12px] text-ink-48 sm:grid-cols-2">
                        <div className="break-all">声明键：{evidence.claimKey}</div>
                        <div className="break-all">来源引用：{evidence.sourceRef}</div>
                        <div className="break-all">来源工具：{evidence.sourceTool}</div>
                        <div className="break-all">位置：{evidence.location || '未提供'}</div>
                        <div className="break-all">内容哈希：{shortId(evidence.contentHash.replace('sha256:', ''))}</div>
                        <div>采集时间：{fmtDateTime(evidence.collectedAt)}</div>
                        <div>脱敏：{evidence.redactionStatus === 'redacted' ? '已脱敏' : '不需要'}</div>
                        <div>截断：{evidence.truncated ? '是' : '否'}</div>
                      </dl>
                    </div>
                  </div>
                </Card>
              </li>
            ))}
          </ol>
        )}
      </section>

      <section className="mb-8 grid gap-5 sm:grid-cols-2">
        <Card className="p-6">
          <CardTitle className="mb-2">Token 使用</CardTitle>
          <dl>
            <MetaRow label="模型调用">{value.usage.modelCalls}</MetaRow>
            <MetaRow label="Prompt Token">{value.usage.promptTokens}</MetaRow>
            <MetaRow label="Completion Token">{value.usage.completionTokens}</MetaRow>
            <MetaRow label="总 Token">{value.usage.totalTokens}</MetaRow>
            <MetaRow label="缓存 / 推理">{value.usage.cachedTokens} / {value.usage.reasoningTokens}</MetaRow>
          </dl>
        </Card>
        <Card className="p-6">
          <CardTitle className="mb-2">运行版本</CardTitle>
          <dl>
            <MetaRow label="模型">{value.modelProvider} / {value.modelId}</MetaRow>
            <MetaRow label="Prompt">{value.promptVersion}</MetaRow>
            <MetaRow label="选择技能">{value.selectedSkill}</MetaRow>
            <MetaRow label="执行技能">{value.executedSkills.join('、') || '无'}</MetaRow>
            <MetaRow label="Agent 运行">{value.agentRuns}</MetaRow>
            <MetaRow label="停止原因">{value.stopReason || '未提供'}</MetaRow>
          </dl>
        </Card>
      </section>

      <section className="mb-8">
        <h2 className="mb-1 text-[19px] font-semibold text-ink">人工复核</h2>
        <p className="mb-4 text-[13px] text-ink-48">复核只评价本报告，不会回写 MES/ERP，也不会自动进入知识库。</p>
        <Card className="p-6">
          {reviews.isPending ? (
            <PageLoading />
          ) : reviews.isError ? (
            <EmptyState
              title="复核历史读取失败"
              description={reviews.error instanceof Error ? reviews.error.message : '请稍后重试'}
              action={<Button variant="neutral" onClick={() => void reviews.refetch()}>重新加载</Button>}
            />
          ) : history.length > 0 ? (
            <div className="mb-6 flex flex-col gap-3">
              {history.map((review) => (
                <div key={review.id} className="rounded-utility bg-pearl px-4 py-3">
                  <div className="mb-1 flex flex-wrap items-center gap-2.5">
                    <Badge tone={verdictMeta[review.verdict].tone}>{verdictMeta[review.verdict].label}</Badge>
                    <span className="text-[12px] text-ink-48">用户 {shortId(review.reviewedBy)} · {fmtDateTime(review.createdAt)}</span>
                  </div>
                  {review.comment && <p className="text-[13px] leading-[1.6] text-ink-80">{review.comment}</p>}
                </div>
              ))}
            </div>
          ) : (
            <p className="mb-6 text-[13px] text-ink-48">还没有复核记录</p>
          )}

          {user?.role === 'admin' ? (
            <div className="rounded-utility bg-pearl px-4 py-3 text-[13px] text-ink-48">管理员可查看复核历史，但不能代替任务创建者提交复核。</div>
          ) : (
            <div className="border-t border-divider pt-5 print:hidden">
              <FieldLabel>提交复核</FieldLabel>
              <div className="mb-4 flex flex-wrap gap-3" role="radiogroup" aria-label="复核结论">
                {verdictOptions.map((option) => (
                  <label key={option.value} className="flex h-10 items-center gap-2 rounded-utility border border-hairline px-4 text-[13px]">
                    <input type="radio" name="review-verdict" value={option.value} checked={verdict === option.value} onChange={() => setVerdict(option.value)} className="size-4 accent-primary" />
                    {option.label}
                  </label>
                ))}
              </div>
              <TextArea placeholder="评论（可选）" maxLength={2000} value={comment} onChange={(event) => setComment(event.target.value)} className="mb-4" />
              <div className="flex justify-end">
                <Button disabled={!verdict || submit.isPending} onClick={() => submit.mutate()}>{submit.isPending ? '提交中…' : '提交复核'}</Button>
              </div>
            </div>
          )}
        </Card>
      </section>

      <p className="pb-4 text-[12px] text-ink-48">报告 {value.reportId} · 生成于 {fmtDateTime(value.generatedAt)} · Schema v{value.reportSchemaVersion}</p>
    </div>
  )
}
