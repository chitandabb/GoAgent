import { useQuery } from '@tanstack/react-query'
import * as api from '@/shared/api'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Card } from '@/shared/ui/Card'
import { PageLoading } from '@/shared/ui/Spinner'

function Row({ label, text }: { label: string; text: string }) {
  return (
    <div>
      <p className="mb-0.5 text-[11px] font-semibold text-ink-48">{label}</p>
      <p className="text-[13px] leading-[1.65] text-ink-80">{text}</p>
    </div>
  )
}

export function CaseCards() {
  const cards = useQuery({ queryKey: ['case-cards'], queryFn: api.listCaseCards })

  if (cards.isPending) return <PageLoading />

  return (
    <div className="grid gap-5 lg:grid-cols-2">
      {(cards.data ?? []).map((c) => (
        <Card key={c.cardId} className="p-6">
          <div className="mb-3 flex items-start justify-between gap-3">
            <div>
              <p className="text-[12px] font-semibold text-ink-48">{c.cardId}</p>
              <h3 className="mt-0.5 text-[16px] font-semibold text-ink">{c.title}</h3>
            </div>
            <Badge tone="blue">
              {c.productName} {c.productVersion}
            </Badge>
          </div>
          <p className="mb-4 text-[12px] text-ink-48">适用环境：{c.environment}</p>
          <div className="flex flex-col gap-3.5">
            <Row label="问题现象" text={c.symptom} />
            <Row label="已确认根因" text={c.rootCause} />
            <Row label="解决方法" text={c.solution} />
            <Row label="验证方式" text={c.verification} />
            <Row label="不适用条件" text={c.notApplicable} />
          </div>
          <p className="mt-4 border-t border-divider pt-3 text-[11px] text-ink-48">
            管理员维护 · 更新于 {fmtDateTime(c.updatedAt)} · AI 报告不会自动回流为案例卡片
          </p>
        </Card>
      ))}
    </div>
  )
}
