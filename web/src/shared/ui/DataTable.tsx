import type { ReactNode } from 'react'
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from '@tanstack/react-table'
import { cn } from '@/shared/lib/utils'
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
  total?: number
  emptyText?: string
  emptyDescription?: string
  emptyAction?: ReactNode
}

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  onRowClick,
  loading = false,
  total,
  emptyText = '暂无数据',
  emptyDescription,
  emptyAction,
}: Props<T>) {
  const columnDefs: ColumnDef<T>[] = columns.map((column) => ({
    id: column.key,
    header: () => column.title,
    cell: ({ row }) => column.render(row.original),
    meta: { className: column.className },
  }))

  const table = useReactTable({
    data: rows,
    columns: columnDefs,
    getRowId: (row) => rowKey(row),
    getCoreRowModel: getCoreRowModel(),
  })

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
          <table data-slot="table" className="w-full min-w-[860px] caption-bottom">
            <thead data-slot="table-header">
              {table.getHeaderGroups().map((headerGroup) => (
                <tr key={headerGroup.id} className="border-b border-divider">
                  {headerGroup.headers.map((header) => (
                    <th
                      key={header.id}
                      className={cn(
                        'h-10 px-4 text-left align-middle text-[12px] font-semibold text-ink-48 first:pl-5 last:pr-5',
                        header.column.columnDef.meta?.className,
                      )}
                    >
                      {header.isPlaceholder
                        ? null
                        : flexRender(header.column.columnDef.header, header.getContext())}
                    </th>
                  ))}
                </tr>
              ))}
            </thead>
            <tbody data-slot="table-body">
              {table.getRowModel().rows.map((row) => (
                <tr
                  key={row.id}
                  onClick={onRowClick ? () => onRowClick(row.original) : undefined}
                  onKeyDown={
                    onRowClick
                      ? (event) => {
                          if (event.key === 'Enter' || event.key === ' ') {
                            event.preventDefault()
                            onRowClick(row.original)
                          }
                        }
                      : undefined
                  }
                  tabIndex={onRowClick ? 0 : undefined}
                  className={cn(
                    'border-b border-divider last:border-0',
                    onRowClick &&
                      'cursor-pointer transition-colors hover:bg-pearl focus-visible:bg-pearl focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary-focus',
                  )}
                >
                  {row.getVisibleCells().map((cell) => (
                    <td
                      key={cell.id}
                      className={cn(
                        'px-4 py-3.5 align-middle text-[14px] text-ink first:pl-5 last:pr-5',
                        cell.column.columnDef.meta?.className,
                      )}
                    >
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
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
          共 {total ?? rows.length} 条
        </div>
      )}
    </Card>
  )
}

declare module '@tanstack/react-table' {
  interface ColumnMeta<TData, TValue> {
    className?: string
  }
}
