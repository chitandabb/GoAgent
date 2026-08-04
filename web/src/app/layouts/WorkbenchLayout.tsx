import { useEffect } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { useAuth } from '@/app/auth'
import { Wordmark } from '@/shared/ui/Wordmark'

// 按路由前缀设置浏览器标签标题
const titleMap: Array<[string, string]> = [
  ['/workbench', '工作台'],
  ['/cases', '外部工单'],
  ['/tasks', '诊断任务'],
  ['/assistant', '知识助手'],
  ['/knowledge', '知识库'],
  ['/admin', '系统管理'],
]

// 规范 global-nav:44px 纯黑顶栏，12px 链接 —— 全站唯一出现纯黑的地方。
const navLinkCls = ({ isActive }: { isActive: boolean }) =>
  `press shrink-0 whitespace-nowrap rounded-utility px-2.5 py-1 text-[12px] transition-colors ${
    isActive ? 'text-white' : 'text-white/60 hover:text-white'
  }`

export function WorkbenchLayout() {
  const { user, logout } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const isWorkbench = location.pathname.startsWith('/workbench')

  useEffect(() => {
    const hit = titleMap.find(([prefix]) => location.pathname.startsWith(prefix))
    document.title = hit ? `${hit[1]} — MESGuard` : 'MESGuard'
    return () => {
      document.title = 'MESGuard'
    }
  }, [location.pathname])

  const handleLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="flex min-h-dvh flex-col">
      <header className="sticky top-0 z-40 bg-nav print:hidden">
        <div className="mx-auto flex h-11 max-w-[1200px] items-center gap-2 px-4 sm:gap-7 sm:px-6">
          <Wordmark on="dark" className="shrink-0 text-[15px]" />
          <nav className="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto">
            <NavLink to="/workbench" className={navLinkCls}>
              工作台
            </NavLink>
            <NavLink to="/cases" className={navLinkCls}>
              外部工单
            </NavLink>
            <NavLink to="/knowledge" className={navLinkCls}>
              知识库
            </NavLink>
            {user?.role === 'admin' && (
              <NavLink to="/admin" className={navLinkCls}>
                系统管理
              </NavLink>
            )}
          </nav>
          <div className="ml-auto flex shrink-0 items-center gap-3">
            <span className="hidden text-[12px] text-white/60 sm:inline">
              {user?.displayName}
              <span className="ml-1.5 text-white/35">
                {user?.role === 'admin' ? 'admin' : 'analyst'}
              </span>
            </span>
            <Link
              to="/change-password"
              className="press hidden rounded-utility px-2 py-1 text-[12px] text-white/60 hover:text-white sm:inline"
            >
              修改密码
            </Link>
            <button
              type="button"
              onClick={handleLogout}
              className="press focus-ring rounded-utility px-2 py-1 text-[12px] text-white/60 hover:text-white"
            >
              退出
            </button>
          </div>
        </div>
      </header>

      <main className={isWorkbench ? 'w-full flex-1' : 'mx-auto w-full max-w-[1200px] flex-1 px-6 py-10'}>
        <Outlet />
      </main>

      <footer className={`${isWorkbench ? 'hidden' : ''} border-t border-hairline print:hidden`}>
        <div className="mx-auto max-w-[1200px] px-6 py-6 text-[12px] text-ink-48">
          MESGuard 工作台 — 诊断结论仅供人工判断，不回写 MES/ERP；尚未接入的功能会明确标记为 Mock 或未实现。
        </div>
      </footer>
    </div>
  )
}
