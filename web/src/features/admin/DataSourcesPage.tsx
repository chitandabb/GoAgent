import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import * as api from '@/shared/api'
import type { CatalogEntry, CatalogVersion } from '@/shared/api'
import {
  catalogStatusMeta,
  scanStatusMeta,
} from '@/shared/lib/status'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { Card, CardTitle } from '@/shared/ui/Card'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { PageLoading } from '@/shared/ui/Spinner'

const sensitivityLabel = {
  public: { label: '公开', tone: 'gray' as const },
  internal: { label: '内部', tone: 'blue' as const },
  sensitive: { label: '敏感', tone: 'red' as const },
}

function EntriesTable({ version }: { version: CatalogVersion }) {
  const qc = useQueryClient()
  const entries = useQuery({
    queryKey: ['catalog-entries', version.versionId],
    queryFn: () => api.listCatalogEntries(version.versionId),
  })
  const editable = version.status === 'draft'

  const toggleQueryable = useMutation({
    mutationFn: (e: CatalogEntry) =>
      api.updateCatalogEntry(e.entryId, { queryable: !e.queryable }),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: ['catalog-entries', version.versionId] }),
  })

  const columns: Column<CatalogEntry>[] = [
    {
      key: 'object',
      title: '表 / 字段',
      render: (e) => (
        <span>
          <span className="text-ink-48">{e.schemaName}.</span>
          <span className="font-semibold">{e.objectName}</span>
          <span className="text-ink-48">.</span>
          {e.columnName}
        </span>
      ),
    },
    {
      key: 'type',
      title: '类型',
      render: (e) => <code className="text-[12px] text-ink-48">{e.dataType}</code>,
    },
    {
      key: 'comment',
      title: '语义说明',
      className: 'max-w-[220px]',
      render: (e) =>
        e.comment ? (
          <div>
            <span>{e.comment}</span>
            {e.semanticAliases.length > 0 && (
              <p className="mt-0.5 text-[11px] text-ink-48">
                别名：{e.semanticAliases.join('、')}
              </p>
            )}
          </div>
        ) : (
          <span className="text-warn">待补充（可由 LLM 生成候选，需人工确认）</span>
        ),
    },
    {
      key: 'sensitivity',
      title: '敏感级',
      render: (e) => (
        <Badge tone={sensitivityLabel[e.sensitivityLevel].tone}>
          {sensitivityLabel[e.sensitivityLevel].label}
        </Badge>
      ),
    },
    {
      key: 'queryable',
      title: '可查询',
      render: (e) => (
        <button
          type="button"
          disabled={!editable || toggleQueryable.isPending}
          onClick={() => editable && toggleQueryable.mutate(e)}
          className={`press focus-ring inline-flex h-7 items-center rounded-full px-3 text-[12px] font-semibold ${
            e.queryable ? 'bg-ok-soft text-ok' : 'bg-[#efeff1] text-[#6e6e73]'
          } ${editable ? '' : 'cursor-default opacity-80'}`}
          title={editable ? '点击切换白名单（仅 draft 可编辑）' : '仅 draft 版本可编辑'}
        >
          {e.queryable ? '在白名单' : '不可查询'}
        </button>
      ),
    },
  ]

  return (
    <DataTable
      columns={columns}
      rows={entries.data ?? []}
      rowKey={(e) => e.entryId}
      loading={entries.isPending}
      emptyText="该版本暂无条目"
    />
  )
}

export function DataSourcesPage() {
  const qc = useQueryClient()
  const [selectedDs, setSelectedDs] = useState('ds-mes-demo')
  const [selectedVersion, setSelectedVersion] = useState<string | null>(null)

  const dataSources = useQuery({
    queryKey: ['admin-data-sources'],
    queryFn: api.listAdminDataSources,
    refetchInterval: 3000,
  })
  const versions = useQuery({
    queryKey: ['catalog-versions', selectedDs],
    queryFn: () => api.listCatalogVersions(selectedDs),
    refetchInterval: 3000,
  })

  const scan = useMutation({
    mutationFn: () => api.startCatalogScan(selectedDs),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['catalog-versions', selectedDs] })
      qc.invalidateQueries({ queryKey: ['admin-data-sources'] })
    },
  })
  const publish = useMutation({
    mutationFn: (versionId: string) => api.publishCatalogVersion(versionId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['catalog-versions', selectedDs] })
      qc.invalidateQueries({ queryKey: ['admin-data-sources'] })
    },
  })

  if (dataSources.isPending) return <PageLoading />

  const versionList = versions.data ?? []
  const activeVersion =
    versionList.find((v) => v.versionId === selectedVersion) ??
    versionList.find((v) => v.status === 'published') ??
    versionList[0]

  return (
    <div className="flex flex-col gap-5">
      <Card className="bg-pearl px-5 py-3.5">
        <p className="text-[12px] leading-[1.7] text-ink-48">
          连接地址与凭证由配置文件装配，不经过本界面。这里只管理安全元数据与 Schema
          Catalog：扫描只读取表结构元数据，发布后的白名单决定 Text-to-SQL
          的查询边界；运行中的诊断继续使用任务创建时绑定的版本。
        </p>
      </Card>

      {/* 数据源卡片 */}
      <div className="grid gap-4 sm:grid-cols-2">
        {(dataSources.data ?? []).map((d) => (
          <button
            key={d.id}
            type="button"
            onClick={() => {
              setSelectedDs(d.id)
              setSelectedVersion(null)
            }}
            className={`press rounded-card border bg-canvas p-5 text-left ${
              d.id === selectedDs
                ? 'border-2 border-primary-focus'
                : 'border-hairline hover:bg-pearl'
            }`}
          >
            <div className="mb-1 flex items-center justify-between gap-3">
              <p className="text-[14px] font-semibold text-ink">{d.name}</p>
              <Badge tone={d.lastCheckStatus === 'up' ? 'green' : 'red'} dot>
                {d.lastCheckStatus === 'up' ? '连通正常' : '连接失败'}
              </Badge>
            </div>
            <p className="text-[12px] text-ink-48">
              sqlserver · {d.environment} · 检查于 {fmtDateTime(d.lastCheckAt)}
            </p>
            <div className="mt-3 flex items-center gap-2 text-[12px]">
              {d.publishedCatalogVersion ? (
                <Badge tone="green">Catalog v{d.publishedCatalogVersion} 已发布</Badge>
              ) : (
                <Badge tone="orange">未发布 Catalog，不可用于 Text-to-SQL</Badge>
              )}
              {d.lastScanStatus && (
                <Badge tone={scanStatusMeta[d.lastScanStatus].tone}>
                  {scanStatusMeta[d.lastScanStatus].label}
                </Badge>
              )}
            </div>
          </button>
        ))}
      </div>

      {/* 版本列表 */}
      <Card className="p-6">
        <div className="mb-4 flex items-center justify-between gap-4">
          <CardTitle>Catalog 版本</CardTitle>
          <Button
            variant="neutral"
            size="sm"
            onClick={() => scan.mutate()}
            disabled={scan.isPending}
          >
            发起 Schema 扫描
          </Button>
        </div>
        {scan.isError && (
          <p className="mb-3 text-[13px] text-danger">
            {scan.error instanceof Error ? scan.error.message : '扫描创建失败'}
          </p>
        )}
        {versionList.length === 0 ? (
          <p className="py-6 text-center text-[13px] text-ink-48">
            尚未扫描；发起 Schema 扫描后生成 draft 版本
          </p>
        ) : (
          <div className="flex flex-col gap-2">
            {versionList.map((v) => (
              <div
                key={v.versionId}
                className={`flex flex-wrap items-center gap-3 rounded-capsule px-4 py-3 ${
                  activeVersion?.versionId === v.versionId ? 'bg-parchment' : 'bg-pearl'
                }`}
              >
                <button
                  type="button"
                  className="press text-[14px] font-semibold text-ink"
                  onClick={() => setSelectedVersion(v.versionId)}
                >
                  v{v.version}
                </button>
                <Badge tone={catalogStatusMeta[v.status].tone}>
                  {catalogStatusMeta[v.status].label}
                </Badge>
                <Badge
                  tone={scanStatusMeta[v.scanStatus].tone}
                  dot={v.scanStatus === 'running'}
                >
                  {scanStatusMeta[v.scanStatus].label}
                </Badge>
                <span className="text-[12px] text-ink-48">
                  {v.entryCount} 条目 · {v.createdBy} · {fmtDateTime(v.createdAt)}
                  {v.publishedAt && ` · 发布于 ${fmtDateTime(v.publishedAt)}`}
                </span>
                <div className="ml-auto flex items-center gap-2">
                  <Button
                    variant="neutral"
                    size="sm"
                    onClick={() => setSelectedVersion(v.versionId)}
                  >
                    查看条目
                  </Button>
                  {v.status === 'draft' && v.scanStatus === 'succeeded' && (
                    <Button
                      size="sm"
                      onClick={() => publish.mutate(v.versionId)}
                      disabled={publish.isPending}
                    >
                      发布
                    </Button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* 条目表 */}
      {activeVersion && (
        <div>
          <div className="mb-3 flex items-center gap-3">
            <h3 className="text-[15px] font-semibold text-ink">
              v{activeVersion.version} 条目
            </h3>
            <Badge tone={catalogStatusMeta[activeVersion.status].tone}>
              {catalogStatusMeta[activeVersion.status].label}
            </Badge>
            {activeVersion.status === 'draft' && (
              <span className="text-[12px] text-ink-48">
                draft 可编辑白名单；发布后不可修改
              </span>
            )}
          </div>
          <EntriesTable version={activeVersion} />
        </div>
      )}
    </div>
  )
}
