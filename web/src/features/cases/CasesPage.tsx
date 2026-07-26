import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import * as api from '@/shared/api'
import type { ExternalCase } from '@/shared/api'
import { caseStatusMeta, priorityMeta } from '@/shared/lib/status'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { FilterChips } from '@/shared/ui/Chips'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { SearchInput, Select } from '@/shared/ui/Field'
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
  const [dataSourceId, setDataSourceId] = useState('ds-mes-demo')

  const dataSources = useQuery({
    queryKey: ['data-sources'],
    queryFn: api.listDataSources,
  })
  const cases = useQuery({
    queryKey: ['external-cases', dataSourceId, status, keyword],
    queryFn: () => api.listExternalCases({ dataSourceId, status, keyword }),
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
    { key: 'customer', title: '客户', render: (c) => c.customerName },
    {
      key: 'product',
      title: '产品',
      render: (c) => (
        <span>
          {c.productName}
          <span className="ml-1 text-[12px] text-ink-48">{c.productVersion}</span>
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

  return (
    <div>
      <PageHeader
        title="外部工单"
        subtitle="实时读取 SQL Server 工单数据（只读）；发起诊断时才创建不可变快照"
      />

      <div className="mb-5 flex flex-wrap items-center gap-3">
        <Select
          value={dataSourceId}
          onChange={(e) => setDataSourceId(e.target.value)}
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
          onChange={(e) => setKeyword(e.target.value)}
          className="w-72"
        />
        <div className="ml-auto">
          <FilterChips options={statusOptions} value={status} onChange={setStatus} />
        </div>
      </div>

      <DataTable
        columns={columns}
        rows={cases.data ?? []}
        rowKey={(c) => c.externalCaseId}
        onRowClick={(c) => navigate(`/cases/${c.externalCaseId}`)}
        loading={cases.isPending}
        emptyText="没有符合条件的工单"
      />
    </div>
  )
}
