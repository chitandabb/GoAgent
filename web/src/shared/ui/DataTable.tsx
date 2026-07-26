import type { ReactNode } from 'react'
import { Card } from './Card'
import { EmptyState } from './EmptyState'
import { Spinner } from './Spinner'

export interface Column<T> {
  key: string
  title: ReactNode
  className?: string
  render: (row: T) => ReactNode
}

interface Props<T> {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string
  onRowClick?: (row: T) => void
  loading?: boolean
  emptyText?: string
  emptyDescription?: string
  emptyAction?: React.ReactNode
}

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  onRowClick,
  loading = false,
  emptyText = '暂无数据',
  emptyDescription,
  emptyAction,
}: Props<T>) {
  return (
    <Card className="overflow-hidden">
      {loading ? (
        <div className="flex justify-center py-16">
          <Spinner />
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={emptyText} description={emptyDescription} action={emptyAction} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-divider">
                {columns.map((c) => (
                  <th
                    key={c.key}
                    className={`px-4 py-3 text-left text-[12px] font-semibold text-ink-48 first:pl-5 last:pr-5 ${c.className ?? ''}`}
                  >
                    {c.title}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={rowKey(row)}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                  onKeyDown={
                    onRowClick
                      ? (e) => {
                          if (e.key === 'Enter') onRowClick(row)
                        }
                      : undefined
                  }
                  tabIndex={onRowClick ? 0 : undefined}
                  className={`border-b border-divider last:border-0 ${
                    onRowClick
                      ? 'cursor-pointer transition-colors hover:bg-pearl focus-visible:bg-pearl focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary-focus'
                      : ''
                  }`}
                >
                  {columns.map((c) => (
                    <td
                      key={c.key}
                      className={`px-4 py-3.5 text-[14px] text-ink first:pl-5 last:pr-5 ${c.className ?? ''}`}
                    >
                      {c.render(row)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {!loading && rows.length > 0 && (
        <div className="border-t border-divider px-5 py-2.5 text-[12px] text-ink-48">
          共 {rows.length} 条
        </div>
      )}
    </Card>
  )
}
