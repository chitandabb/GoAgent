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
    <div>
      <PageHeader
        title="系统管理"
        subtitle="用户、依赖状态与运行指标（演示数据，部分管理操作为演示实现）"
      />
      <div className="mb-6 flex items-center gap-2">
        <NavLink to="/admin/users" className={tabCls}>
          用户管理
        </NavLink>
        <NavLink to="/admin/data-sources" className={tabCls}>
          数据源与 Catalog
        </NavLink>
        <NavLink to="/admin/system" className={tabCls}>
          系统状态
        </NavLink>
      </div>
      <Outlet />
    </div>
  )
}
