import { useEffect } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { useAuth } from '@/app/auth'
import { Wordmark } from '@/shared/ui/Wordmark'

// 按路由前缀设置浏览器标签标题
const titleMap: Array<[string, string]> = [
  ['/cases', '外部工单'],
  ['/tasks', '诊断任务'],
  ['/assistant', '知识助手'],
  ['/knowledge', '知识库'],
  ['/admin', '系统管理'],
]

// 规范 global-nav:44px 纯黑顶栏，12px 链接 —— 全站唯一出现纯黑的地方。
const navLinkCls = ({ isActive }: { isActive: boolean }) =>
  `press rounded-utility px-2.5 py-1 text-[12px] transition-colors ${
    isActive ? 'text-white' : 'text-white/60 hover:text-white'
  }`

export function WorkbenchLayout() {
  const { user, logout } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()

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
        <div className="mx-auto flex h-11 max-w-[1200px] items-center gap-7 px-6">
          <Wordmark on="dark" className="text-[15px]" />
          <nav className="flex items-center gap-1.5">
            <NavLink to="/cases" className={navLinkCls}>
              外部工单
            </NavLink>
            <NavLink to="/tasks" className={navLinkCls}>
              诊断任务
            </NavLink>
            <NavLink to="/assistant" className={navLinkCls}>
              知识助手
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
          <div className="ml-auto flex items-center gap-3">
            <span className="text-[12px] text-white/60">
              {user?.displayName}
              <span className="ml-1.5 text-white/35">
                {user?.role === 'admin' ? 'admin' : 'analyst'}
              </span>
            </span>
            <Link
              to="/change-password"
              className="press rounded-utility px-2 py-1 text-[12px] text-white/60 hover:text-white"
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

      <main className="mx-auto w-full max-w-[1200px] flex-1 px-6 py-10">
        <Outlet />
      </main>

      <footer className="border-t border-hairline print:hidden">
        <div className="mx-auto max-w-[1200px] px-6 py-6 text-[12px] text-ink-48">
          MESGuard 前端原型 — 当前数据为本地模拟，不代表真实工单；诊断结论仅供人工判断，不回写
          MES/ERP。
        </div>
      </footer>
    </div>
  )
}
