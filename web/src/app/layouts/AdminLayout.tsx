import { NavLink, Outlet } from 'react-router'
import { PageHeader } from '@/shared/ui/PageHeader'

const tabCls = ({ isActive }: { isActive: boolean }) =>
  `press focus-ring h-8 rounded-full px-4 text-[13px] ${
    isActive
      ? 'bg-ink text-white'
      : 'border border-hairline bg-canvas text-ink-80 hover:bg-pearl'
  }`

export function AdminLayout() {
  return (
    <div className="flex min-h-full flex-col">
      <PageHeader
        title="系统管理"
        subtitle="集中维护账号权限、业务数据源与运行状态"
      />
      <div className="mb-6 flex shrink-0 items-center gap-2 overflow-x-auto">
        <NavLink to="/admin/users" className={tabCls}>
          账号与权限
        </NavLink>
        <NavLink to="/admin/data-sources" className={tabCls}>
          业务数据源
        </NavLink>
        <NavLink to="/admin/system" className={tabCls}>
          运行状态
        </NavLink>
      </div>
      <div className="min-h-0 flex-1">
        <Outlet />
      </div>
    </div>
  )
}
