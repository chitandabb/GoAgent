import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as api from '@/shared/api'
import type { EvidenceItem, ReviewVerdict } from '@/shared/api'
import {
  conclusionMeta,
  evidenceSourceMeta,
  riskMeta,
  verdictMeta,
} from '@/shared/lib/status'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card, CardTitle } from '@/shared/ui/Card'
import { FieldLabel, TextArea } from '@/shared/ui/Field'
import { PageLoading } from '@/shared/ui/Spinner'
import { Wordmark } from '@/shared/ui/Wordmark'

const verdictOptions: { value: ReviewVerdict; label: string }[] = [
  { value: 'adopted', label: '采纳' },
  { value: 'partially_adopted', label: '部分采纳' },
  { value: 'rejected', label: '驳回' },
]

function BusinessItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Card className="p-6">
      <CardTitle className="mb-2 text-ink-48">{label}</CardTitle>
      <p className="text-[14px] leading-[1.7] text-ink">{children}</p>
    </Card>
  )
}

/** 证据的可读短名：受控查询 #1 / 案例卡片 KB-0173 / 工单快照 */
function evidenceShortName(ev: EvidenceItem): string {
  if (ev.sourceType === 'sql_query' || ev.sourceType === 'knowledge_case') {
    const seg = ev.location.split('·').pop()?.trim()
    if (seg) return seg
  }
  return evidenceSourceMeta[ev.sourceType].label
}

// 结论状态是报告的“判定”：用色彩层级区分，不与风险/反馈徽章混排。
const verdictColor: Record<string, string> = {
  conclusive: 'text-ok',
  probable: 'text-warn',
  inconclusive: 'text-ink-48',
}

export function ReportPage() {
  const { taskId = '' } = useParams()
  const qc = useQueryClient()
  const [verdict, setVerdict] = useState<ReviewVerdict | null>(null)
  const [comment, setComment] = useState('')

  const report = useQuery({
    queryKey: ['report', taskId],
    queryFn: () => api.getReportByTask(taskId),
  })

  const submit = useMutation({
    mutationFn: () => api.submitReview(report.data!.reportId, verdict!, comment),
    onSuccess: () => {
      setVerdict(null)
      setComment('')
      qc.invalidateQueries({ queryKey: ['report', taskId] })
    },
  })

  if (report.isPending) return <PageLoading />
  if (report.isError || !report.data) {
    return <p className="py-24 text-center text-ink-48">报告不存在或任务尚未完成</p>
  }
  const r = report.data
  const conclusion = conclusionMeta[r.conclusionStatus]
  const risk = riskMeta[r.riskLevel]
  const latestReview = r.reviews.at(-1)

  return (
    <div className="mx-auto max-w-[880px]">
      <div className="mb-4 flex items-center justify-between print:hidden">
        <Link
          to={`/tasks/${taskId}`}
          className="press text-[13px] text-primary"
        >
          ‹ 返回任务详情
        </Link>
        <Button variant="neutral" size="sm" onClick={() => window.print()}>
          打印 / 导出 PDF
        </Button>
      </div>
      {/* ── 判定条：结论状态领衔，风险与反馈降为元数据 ── */}
      {/* print-only 页眉:PDF 脱离系统后仍可溯源 */}
      <div className="mb-6 hidden items-baseline justify-between border-b border-hairline pb-4 print:flex">
        <Wordmark className="text-[18px]" />
        <span className="text-[12px] text-ink-48">
          诊断报告 {r.reportId} · 生成于 {fmtDateTime(r.generatedAt)}
        </span>
      </div>

      <header className="mb-8">
        <p className="mb-2 text-[13px] font-semibold text-ink-48">诊断报告</p>
        <Card className="p-8">
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <p className="text-[12px] font-semibold text-ink-48">结论状态</p>
              <p
                className={`mt-1 text-[30px] font-semibold leading-[1.15] ${verdictColor[r.conclusionStatus]}`}
              >
                {conclusion.label}
              </p>
              {r.conclusionStatus === 'inconclusive' && (
                <p className="mt-1.5 text-[13px] text-ink-48">
                  任务正常完成；当前证据无法支撑可靠结论，系统拒绝推测。
                </p>
              )}
            </div>
            <div className="flex items-center gap-2 pt-1.5">
              <Badge tone={risk.tone}>{risk.label}</Badge>
              {latestReview && (
                <Badge tone={verdictMeta[latestReview.verdict].tone}>
                  {verdictMeta[latestReview.verdict].label}
                </Badge>
              )}
            </div>
          </div>
          {/* 业务摘要层：面向非技术人员的阅读态 */}
          <p className="reading mt-6 border-t border-divider pt-6">
            {r.business.overview}
          </p>
        </Card>
      </header>

      {r.conclusionStatus === 'inconclusive' && r.inconclusiveDetail ? (
        <div className="mb-10">
          <div className="grid gap-5 sm:grid-cols-3">
            {(
              [
                ['已检查', r.inconclusiveDetail.checked],
                ['仍缺少', r.inconclusiveDetail.missing],
                ['下一步建议', r.inconclusiveDetail.nextSteps],
              ] as const
            ).map(([label, items]) => (
              <Card key={label} className="p-6">
                <CardTitle className="mb-3 text-ink-48">{label}</CardTitle>
                <ul className="flex list-disc flex-col gap-2 pl-4 text-[13px] leading-[1.65] text-ink-80">
                  {items.map((it) => (
                    <li key={it}>{it}</li>
                  ))}
                </ul>
              </Card>
            ))}
          </div>
        </div>
      ) : (
        <div className="mb-10 grid gap-5 sm:grid-cols-2">
          <BusinessItem label="最可能原因">{r.business.likelyCause}</BusinessItem>
          <BusinessItem label="影响范围">{r.business.impact}</BusinessItem>
          <BusinessItem label="建议处理">{r.business.suggestion}</BusinessItem>
          <BusinessItem label="是否需要开发介入">
            {r.business.needDeveloper
              ? '需要。部分结论涉及代码写入逻辑，建议由开发人员按技术证据层继续分析（L3）。'
              : '暂不需要，可先按建议在业务或管理界面处理（L1/L2）。'}
          </BusinessItem>
        </div>
      )}

      {/* ── 技术证据层 ── */}
      <h2 className="mb-1 text-[21px] font-semibold text-ink">技术证据层</h2>
      <p className="mb-5 text-[13px] text-ink-48">
        每条结论均引用具体证据；无法定位证据的内容不会出现在结论中。
      </p>

      <div className="mb-5 flex flex-col gap-3">
        {r.claims.map((claim, i) => (
          <Card key={claim.claimId} className="p-5">
            <div className="flex gap-3.5">
              <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-info-soft text-[12px] font-semibold text-primary">
                {i + 1}
              </span>
              <div className="min-w-0">
                <p className="text-[14px] font-semibold leading-[1.55] text-ink">
                  {claim.statement}
                </p>
                <div className="mt-2 flex flex-wrap gap-2">
                  {claim.evidenceIds.map((eid) => {
                    const ev = r.evidence.find((x) => x.evidenceId === eid)
                    if (!ev) return null
                    return (
                      <a
                        key={eid}
                        href={`#${eid}`}
                        className="press text-[12px] text-primary hover:underline"
                      >
                        证据 · {evidenceShortName(ev)}
                      </a>
                    )
                  })}
                </div>
              </div>
            </div>
          </Card>
        ))}
      </div>

      <div className="mb-5 flex flex-col gap-3">
        {r.evidence.map((ev) => (
          <Card key={ev.evidenceId} className="scroll-mt-20 bg-pearl p-5" >
            <div id={ev.evidenceId} className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <div className="mb-1.5 flex items-center gap-2">
                  <Badge tone={evidenceSourceMeta[ev.sourceType].tone}>
                    {evidenceSourceMeta[ev.sourceType].label}
                  </Badge>
                  <span className="text-[12px] text-ink-48">{ev.location}</span>
                </div>
                <p className="text-[13px] leading-[1.65] text-ink-80">{ev.summary}</p>
              </div>
              <time className="shrink-0 text-[12px] text-ink-48">
                {fmtDateTime(ev.collectedAt)}
              </time>
            </div>
          </Card>
        ))}
      </div>

      {r.limitations.length > 0 && (
        <div className="mb-10 rounded-card border border-warn/25 bg-warn-soft px-6 py-5">
          <p className="mb-2 text-[13px] font-semibold text-warn">限制条件</p>
          <ul className="flex list-disc flex-col gap-1 pl-5 text-[13px] leading-[1.65] text-ink-80">
            {r.limitations.map((l) => (
              <li key={l}>{l}</li>
            ))}
          </ul>
        </div>
      )}

      {/* ── 反馈 ── */}
      <h2 className="mb-1 text-[21px] font-semibold text-ink">报告反馈</h2>
      <p className="mb-5 text-[13px] text-ink-48">
        反馈用于评估诊断效果，不会回写 MES/ERP，也不会自动进入知识库。
      </p>

      <Card className="mb-5 p-6">
        {r.reviews.length > 0 && (
          <div className="mb-6 flex flex-col gap-3">
            {r.reviews
              .slice()
              .reverse()
              .map((rv) => (
                <div key={rv.reviewId} className="rounded-capsule bg-pearl px-4 py-3">
                  <div className="mb-1 flex items-center gap-2.5">
                    <Badge tone={verdictMeta[rv.verdict].tone}>
                      {verdictMeta[rv.verdict].label}
                    </Badge>
                    <span className="text-[12px] text-ink-48">
                      {rv.reviewedBy} · {fmtDateTime(rv.reviewedAt)}
                    </span>
                  </div>
                  {rv.comment && (
                    <p className="text-[13px] leading-[1.6] text-ink-80">{rv.comment}</p>
                  )}
                </div>
              ))}
          </div>
        )}

        <div className="print:hidden">
        <FieldLabel>提交新反馈</FieldLabel>
        <div className="mb-4 flex gap-2">
          {verdictOptions.map((o) => (
            <button
              key={o.value}
              type="button"
              onClick={() => setVerdict(o.value)}
              className={`press focus-ring h-9 rounded-full border px-5 text-[13px] ${
                verdict === o.value
                  ? 'border-2 border-primary-focus font-semibold text-ink'
                  : 'border-hairline text-ink-80 hover:bg-pearl'
              }`}
            >
              {o.label}
            </button>
          ))}
        </div>
        <TextArea
          placeholder="补充说明（可选）"
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          className="mb-4"
        />
        <div className="flex justify-end">
          <Button
            disabled={!verdict || submit.isPending}
            onClick={() => submit.mutate()}
          >
            {submit.isPending ? '提交中…' : '提交反馈'}
          </Button>
        </div>
        </div>
      </Card>

      <p className="pb-4 text-[12px] text-ink-48">
        报告 {r.reportId} · 生成于 {fmtDateTime(r.generatedAt)} · 模型 {r.modelVersion}
      </p>
    </div>
  )
}
