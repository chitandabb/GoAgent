import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronUp } from 'lucide-react'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type { ReviewVerdict } from '@/shared/api/m1-types'
import { conclusionMeta, riskMeta, verdictMeta } from '@/shared/lib/status'
import { fmtDateTime, shortId } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { EmptyState } from '@/shared/ui/EmptyState'
import { TextArea } from '@/shared/ui/Field'
import { PageLoading } from '@/shared/ui/Spinner'
import { useToast } from '@/shared/ui/Toast'

const verdictOptions: { value: ReviewVerdict; label: string }[] = [
  { value: 'adopted', label: '采纳' },
  { value: 'partially_adopted', label: '部分采纳' },
  { value: 'rejected', label: '驳回' },
]

const confidenceLabels = { high: '高', medium: '中', low: '低' } as const

function MetaItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-3 border-b border-divider py-2 last:border-0">
      <dt className="shrink-0 text-[10px] text-ink-48">{label}</dt>
      <dd className="min-w-0 break-words text-right text-[11px] text-ink-80">{children}</dd>
    </div>
  )
}

export function InlineReport({ taskId }: { taskId: string }) {
  const { user } = useAuth()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [expanded, setExpanded] = useState(false)
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
      toast.success('复核已提交，历史记录已刷新')
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : '复核提交失败'),
  })

  if (report.isPending) return <div className="border-t border-divider"><PageLoading /></div>
  if (report.isError || !report.data) {
    return (
      <div className="border-t border-divider px-5 py-4">
        <EmptyState
          title="正式报告不可读取"
          description={report.error instanceof Error ? report.error.message : '请稍后重试'}
          action={<Button size="sm" variant="neutral" onClick={() => void report.refetch()}>重新加载</Button>}
        />
      </div>
    )
  }

  const value = report.data
  const currentReview = reviews.data?.current
  return (
    <div className="border-t border-divider bg-pearl/70">
      <div className="px-5 py-5 sm:px-6">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <p className="text-[11px] font-semibold text-ink-48">正式诊断报告</p>
            <div className="mt-1 flex flex-wrap items-center gap-2">
              <h4 className="text-[20px] font-semibold text-ink">{conclusionMeta[value.conclusionStatus].label}</h4>
              <Badge tone={riskMeta[value.riskLevel].tone}>{riskMeta[value.riskLevel].label}</Badge>
              {currentReview && <Badge tone={verdictMeta[currentReview.verdict].tone}>{verdictMeta[currentReview.verdict].label}</Badge>}
            </div>
          </div>
          <Button variant="neutral" size="sm" onClick={() => setExpanded((current) => !current)}>
            {expanded ? <ChevronUp /> : <ChevronDown />}
            {expanded ? '收起报告' : '展开报告'}
          </Button>
        </div>
        <p className="mt-4 whitespace-pre-wrap text-[14px] leading-[1.75] text-ink">{value.conclusion}</p>
      </div>

      {expanded && (
        <div className="border-t border-divider px-5 py-5 sm:px-6">
          <div className="grid gap-5 md:grid-cols-2">
            <section>
              <h5 className="mb-2 text-[12px] font-semibold text-ink">业务摘要</h5>
              <p className="whitespace-pre-wrap text-[13px] leading-[1.7] text-ink-80">{value.businessSummary}</p>
            </section>
            <section>
              <h5 className="mb-2 text-[12px] font-semibold text-ink">技术摘要</h5>
              <p className="whitespace-pre-wrap text-[13px] leading-[1.7] text-ink-80">{value.technicalSummary}</p>
            </section>
            <section>
              <h5 className="mb-2 text-[12px] font-semibold text-ink">结论质量</h5>
              <dl>
                <MetaItem label="置信度">{confidenceLabels[value.confidence]}</MetaItem>
                <MetaItem label="报告完整性">{value.partial ? '部分报告' : '完整报告'}</MetaItem>
                <MetaItem label="停止原因">{value.stopReason || '未提供'}</MetaItem>
              </dl>
            </section>
            <section>
              <h5 className="mb-2 text-[12px] font-semibold text-ink">限制条件</h5>
              {value.limitations.length > 0 ? <ul className="list-disc space-y-1.5 pl-4 text-[12px] leading-[1.6] text-ink-80">{value.limitations.map((item, index) => <li key={`${item}-${index}`}>{item}</li>)}</ul> : <p className="text-[12px] text-ink-48">未声明额外限制</p>}
            </section>
            <section>
              <h5 className="mb-2 text-[12px] font-semibold text-ink">缺失证据</h5>
              {value.missingEvidence.length > 0 ? <ul className="list-disc space-y-1.5 pl-4 text-[12px] leading-[1.6] text-ink-80">{value.missingEvidence.map((item, index) => <li key={`${item}-${index}`}>{item}</li>)}</ul> : <p className="text-[12px] text-ink-48">未声明缺失证据</p>}
            </section>
          </div>

          <section className="mt-6 grid gap-5 border-t border-divider pt-5 sm:grid-cols-2">
            <div>
              <h5 className="mb-2 text-[12px] font-semibold text-ink">Token 使用</h5>
              <dl>
                <MetaItem label="模型调用">{value.usage.modelCalls}</MetaItem>
                <MetaItem label="Prompt / Completion">{value.usage.promptTokens} / {value.usage.completionTokens}</MetaItem>
                <MetaItem label="缓存 / 推理">{value.usage.cachedTokens} / {value.usage.reasoningTokens}</MetaItem>
                <MetaItem label="总 Token">{value.usage.totalTokens}</MetaItem>
              </dl>
            </div>
            <div>
              <h5 className="mb-2 text-[12px] font-semibold text-ink">运行版本</h5>
              <dl>
                <MetaItem label="模型">{value.modelProvider} / {value.modelId}</MetaItem>
                <MetaItem label="Prompt">{value.promptVersion}</MetaItem>
                <MetaItem label="选择技能">{value.selectedSkill}</MetaItem>
                <MetaItem label="执行技能">{value.executedSkills.join('、') || '无'}</MetaItem>
                <MetaItem label="Agent 运行">{value.agentRuns}</MetaItem>
              </dl>
            </div>
          </section>

          <section className="mt-6 border-t border-divider pt-5">
            <div className="mb-3 flex items-end justify-between gap-4">
              <div>
                <h5 className="text-[12px] font-semibold text-ink">有序证据声明</h5>
                <p className="mt-1 text-[10px] text-ink-48">只展示声明与溯源元数据，不展示或补造原始证据正文。</p>
              </div>
              <span className="text-[10px] text-ink-48">{value.evidence.length} 条</span>
            </div>
            <ol className="flex flex-col gap-2">
              {value.evidence.map((evidence, index) => (
                <li key={`${evidence.evidenceId}-${evidence.claimKey}-${index}`} className="rounded-utility border border-hairline bg-canvas px-4 py-3">
                  <div className="flex items-start gap-3">
                    <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-info-soft text-[10px] font-semibold text-primary">{index + 1}</span>
                    <div className="min-w-0 flex-1">
                      <p className="text-[12px] font-semibold leading-[1.55] text-ink">{evidence.claim}</p>
                      <p className="mt-1 break-all text-[10px] text-ink-48">{evidence.sourceTool} · {evidence.sourceRef}</p>
                    </div>
                  </div>
                </li>
              ))}
            </ol>
          </section>

          <section className="mt-6 border-t border-divider pt-5">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
              <div>
                <h5 className="text-[12px] font-semibold text-ink">人工复核</h5>
                <p className="mt-1 text-[10px] text-ink-48">复核不会回写 MES/ERP。</p>
              </div>
              <span className="text-[10px] text-ink-48">报告 {shortId(value.reportId)} · {fmtDateTime(value.generatedAt)}</span>
            </div>

            {(reviews.data?.items ?? []).length > 0 && (
              <div className="mb-4 flex flex-col gap-2">
                {reviews.data!.items.map((review) => (
                  <div key={review.id} className="rounded-utility bg-canvas px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone={verdictMeta[review.verdict].tone}>{verdictMeta[review.verdict].label}</Badge>
                      <span className="text-[10px] text-ink-48">{fmtDateTime(review.createdAt)}</span>
                    </div>
                    {review.comment && <p className="mt-1 text-[11px] leading-[1.55] text-ink-80">{review.comment}</p>}
                  </div>
                ))}
              </div>
            )}

            {reviews.isError ? (
              <p className="text-[12px] text-danger">{reviews.error instanceof Error ? reviews.error.message : '复核历史读取失败'}</p>
            ) : user?.role === 'admin' ? (
              <p className="rounded-utility bg-canvas px-3 py-2.5 text-[11px] text-ink-48">管理员只能查看复核历史，不能代替创建者提交。</p>
            ) : (
              <div>
                <div className="mb-3 flex flex-wrap gap-2" role="radiogroup" aria-label="复核结论">
                  {verdictOptions.map((option) => (
                    <label key={option.value} className="flex h-8 items-center gap-2 rounded-full border border-hairline bg-canvas px-3 text-[11px]">
                      <input type="radio" name={`review-${taskId}`} checked={verdict === option.value} onChange={() => setVerdict(option.value)} className="size-3.5 accent-primary" />
                      {option.label}
                    </label>
                  ))}
                </div>
                <TextArea value={comment} onChange={(event) => setComment(event.target.value)} maxLength={2000} placeholder="复核评论（可选）" className="min-h-20" />
                <div className="mt-3 flex justify-end"><Button size="sm" disabled={!verdict || submit.isPending} onClick={() => submit.mutate()}>{submit.isPending ? '提交中…' : '提交复核'}</Button></div>
              </div>
            )}
          </section>
        </div>
      )}
    </div>
  )
}
