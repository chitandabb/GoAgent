import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import * as api from '@/shared/api'
import type { ExternalCase } from '@/shared/api/m1-types'
import { caseStatusMeta, priorityMeta } from '@/shared/lib/status'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { FilterChips } from '@/shared/ui/Chips'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { SearchInput, Select } from '@/shared/ui/Field'
import { Button } from '@/shared/ui/Button'
import { EmptyState } from '@/shared/ui/EmptyState'
import { PageHeader } from '@/shared/ui/PageHeader'

const statusOptions = [
  { value: 'all', label: '全部' },
  { value: 'open', label: '待处理' },
  { value: 'processing', label: '处理中' },
  { value: 'closed', label: '已关闭' },
]

export function CasesPage() {
  const navigate = useNavigate()
  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState('all')
  const [dataSourceId, setDataSourceId] = useState('')
  const [page, setPage] = useState(1)

  const dataSources = useQuery({
    queryKey: ['data-sources'],
    queryFn: api.listDataSources,
  })
  useEffect(() => {
    if (!dataSourceId && dataSources.data?.[0]) {
      setDataSourceId(dataSources.data[0].id)
    }
  }, [dataSourceId, dataSources.data])

  const cases = useQuery({
    queryKey: ['external-cases', dataSourceId, status, keyword, page],
    queryFn: () =>
      api.listExternalCases({
        dataSourceId,
        status: status === 'all' ? undefined : (status as 'open' | 'processing' | 'closed'),
        keyword: keyword.trim() || undefined,
        page,
        pageSize: 20,
      }),
    enabled: !!dataSourceId,
  })

  const columns: Column<ExternalCase>[] = [
    {
      key: 'key',
      title: '工单号',
      render: (c) => (
        <span className="font-semibold text-ink">{c.externalCaseKey}</span>
      ),
    },
    {
      key: 'title',
      title: '标题',
      className: 'max-w-[320px]',
      render: (c) => <span className="line-clamp-1">{c.title}</span>,
    },
    { key: 'customer', title: '客户', render: (c) => c.customerName || '—' },
    {
      key: 'product',
      title: '产品',
      render: (c) => (
        <span>
          {c.productName || '—'}
          {c.productVersion && (
            <span className="ml-1 text-[12px] text-ink-48">{c.productVersion}</span>
          )}
        </span>
      ),
    },
    {
      key: 'priority',
      title: '优先级',
      render: (c) => (
        <Badge tone={priorityMeta[c.priority].tone}>{priorityMeta[c.priority].label}</Badge>
      ),
    },
    {
      key: 'status',
      title: '状态',
      render: (c) => (
        <Badge tone={caseStatusMeta[c.status].tone}>{caseStatusMeta[c.status].label}</Badge>
      ),
    },
    {
      key: 'reportedAt',
      title: '上报时间',
      className: 'text-ink-48',
      render: (c) => fmtDateTime(c.reportedAt),
    },
  ]

  const rows = cases.data?.items ?? []
  const total = cases.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / 20))

  if (dataSources.isError) {
    return (
      <div>
        <PageHeader title="工单" subtitle="选择一条客户反馈，整理成开发可跟进的排查单" />
        <EmptyState
          title="数据源暂时不可用"
          description={dataSources.error instanceof Error ? dataSources.error.message : '无法读取数据源'}
          action={<Button onClick={() => void dataSources.refetch()}>重新加载</Button>}
        />
      </div>
    )
  }

  return (
    <div>
      <PageHeader
        title="工单"
        subtitle="选择一条客户反馈，整理成开发可跟进的排查单"
      />

      <div className="mb-5 flex flex-wrap items-center gap-3">
        <Select
          value={dataSourceId}
          onValueChange={(value) => {
            setDataSourceId(value)
            setPage(1)
          }}
          className="!w-56"
        >
          {(dataSources.data ?? []).map((d) => (
            <option key={d.id} value={d.id}>
              {d.name}
            </option>
          ))}
        </Select>
        <SearchInput
          placeholder="搜索工单号、标题或客户"
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="w-72"
        />
        <div className="ml-auto">
          <FilterChips
            options={statusOptions}
            value={status}
            onChange={(value) => {
              setStatus(value)
              setPage(1)
            }}
          />
        </div>
      </div>

      {dataSources.data?.length === 0 ? (
        <EmptyState
          title="没有可用数据源"
          description="当前没有已启用的业务数据源，暂时无法读取工单。"
        />
      ) : cases.isError ? (
        <EmptyState
          title="工单读取失败"
          description={cases.error instanceof Error ? cases.error.message : '请稍后重试'}
          action={<Button onClick={() => void cases.refetch()}>重新加载</Button>}
        />
      ) : (
        <>
          <DataTable
            columns={columns}
            rows={rows}
            total={total}
            rowKey={(c) => c.externalCaseId}
            onRowClick={(c) => navigate(`/cases/${c.externalCaseId}`)}
            loading={cases.isPending}
            emptyText="没有符合条件的工单"
          />
          {totalPages > 1 && (
            <div className="mt-4 flex items-center justify-between gap-4 text-[12px] text-ink-48">
              <span>第 {page} / {totalPages} 页</span>
              <div className="flex gap-2">
                <Button variant="neutral" size="sm" disabled={page <= 1} onClick={() => setPage((value) => value - 1)}>
                  上一页
                </Button>
                <Button variant="neutral" size="sm" disabled={page >= totalPages} onClick={() => setPage((value) => value + 1)}>
                  下一页
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
