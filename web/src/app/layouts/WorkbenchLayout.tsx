import type { LucideIcon } from 'lucide-react'
import {
  ClipboardList,
  Library,
  ListChecks,
  LogOut,
  MessageSquareText,
  Settings2,
} from 'lucide-react'
import { useEffect } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router'
import { useAuth } from '@/app/auth'
import { Wordmark } from '@/shared/ui/Wordmark'

type NavigationItem = {
  to: string
  label: string
  icon: LucideIcon
}

const navigation: NavigationItem[] = [
  { to: '/cases', label: '工单', icon: ClipboardList },
  { to: '/tasks', label: '排查任务', icon: ListChecks },
  { to: '/assistant', label: '助手', icon: MessageSquareText },
  { to: '/knowledge', label: '知识库', icon: Library },
]

const administration: NavigationItem[] = [
  { to: '/admin', label: '系统管理', icon: Settings2 },
]

const titleMap: Array<[string, string]> = [
  ['/workbench', '问题整理工作台'],
  ['/cases', '工单'],
  ['/tasks', '排查任务'],
  ['/assistant', '助手'],
  ['/knowledge', '知识库'],
  ['/admin', '系统管理'],
]

function NavigationLink({ item }: { item: NavigationItem }) {
  const Icon = item.icon
  return (
    <NavLink
      to={item.to}
      className={({ isActive }) =>
        `group flex items-center gap-3 rounded-utility px-3 py-2.5 transition-colors ${
          isActive
            ? 'bg-white/12 text-white shadow-sm'
            : 'text-sidebar-muted hover:bg-white/7 hover:text-white'
        }`
      }
    >
      {({ isActive }) => (
        <>
          <span
            className={`flex size-8 shrink-0 items-center justify-center rounded-utility ${
              isActive ? 'bg-primary-on-dark/15 text-primary-on-dark' : 'bg-white/5 text-sidebar-muted'
            }`}
          >
            <Icon className="size-4" strokeWidth={1.8} />
          </span>
          <span className="min-w-0 truncate text-[13px] font-semibold leading-5">{item.label}</span>
        </>
      )}
    </NavLink>
  )
}

export function WorkbenchLayout() {
  const { user, logout } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const isWorkbench = location.pathname.startsWith('/workbench')
  const isFixedWorkspace = isWorkbench || location.pathname.startsWith('/assistant')
  const currentTitle = titleMap.find(([prefix]) => location.pathname.startsWith(prefix))?.[1] ?? 'MESGuard'

  useEffect(() => {
    document.title = currentTitle === 'MESGuard' ? currentTitle : `${currentTitle} — MESGuard`
    return () => {
      document.title = 'MESGuard'
    }
  }, [currentTitle])

  const handleLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="flex h-dvh min-h-0 w-full min-w-0 flex-col overflow-hidden bg-parchment text-ink lg:flex-row">
      <aside className="hidden h-full min-h-0 w-56 shrink-0 bg-sidebar text-white lg:block">
        <div className="flex h-full min-h-0 flex-col">
          <div className="border-b border-sidebar-line px-5 py-4">
            <Wordmark on="dark" className="text-[17px]" />
          </div>

          <nav className="min-h-0 flex-1 overflow-y-auto px-3 py-5" aria-label="主导航">
            <p className="mb-2 px-3 text-[10px] font-semibold tracking-[0.12em] text-sidebar-muted/60">工作</p>
            <div className="flex flex-col gap-1">
              {navigation.map((item) => <NavigationLink key={item.to} item={item} />)}
            </div>

            {user?.role === 'admin' && (
              <>
                <p className="mb-2 mt-7 px-3 text-[10px] font-semibold tracking-[0.12em] text-sidebar-muted/60">系统</p>
                <div className="flex flex-col gap-1">
                  {administration.map((item) => <NavigationLink key={item.to} item={item} />)}
                </div>
              </>
            )}
          </nav>

          <div className="border-t border-sidebar-line px-3 py-4">
            <div className="flex items-center gap-3 rounded-utility px-3 py-2">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-white/12 text-[11px] font-semibold text-primary-on-dark">
                {(user?.displayName ?? 'M').slice(0, 1)}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[12px] font-semibold text-white">{user?.displayName}</span>
                <span className="block text-[10px] text-sidebar-muted">{user?.role === 'admin' ? '系统管理员' : '业务人员'}</span>
              </span>
              <button
                type="button"
                onClick={handleLogout}
                className="focus-ring rounded-utility p-1.5 text-sidebar-muted hover:bg-white/10 hover:text-white"
                title="退出登录"
              >
                <LogOut className="size-3.5" />
                <span className="sr-only">退出登录</span>
              </button>
            </div>
          </div>
        </div>
      </aside>

      <div className="flex min-h-0 w-full min-w-0 flex-1 flex-col overflow-hidden">
        <header className="z-40 shrink-0 border-b border-hairline bg-canvas/90 backdrop-blur-md print:hidden">
          <div className="flex h-12 items-center justify-between gap-4 px-4 sm:px-6 lg:px-8">
            <div className="flex min-w-0 items-center gap-3">
              <Link to="/cases" className="lg:hidden">
                <Wordmark className="text-[15px]" />
              </Link>
              <span className="hidden h-5 w-px bg-divider lg:block" />
              <div className="min-w-0">
                <p className="truncate text-[13px] font-semibold text-ink">{currentTitle}</p>
              </div>
            </div>

            <div className="flex shrink-0 items-center gap-2">
              <Link
                to="/change-password"
                className="focus-ring hidden rounded-utility px-2 py-1.5 text-[11px] font-semibold text-ink-48 hover:bg-pearl hover:text-ink sm:inline"
              >
                账户设置
              </Link>
              <button
                type="button"
                onClick={handleLogout}
                className="focus-ring rounded-utility px-2 py-1.5 text-[11px] font-semibold text-ink-48 hover:bg-pearl hover:text-ink sm:hidden"
              >
                退出
              </button>
            </div>
          </div>
        </header>

        <nav className="flex shrink-0 gap-1 overflow-x-auto border-b border-divider bg-canvas px-3 py-2 lg:hidden" aria-label="主导航">
          {navigation.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                `whitespace-nowrap rounded-utility px-3 py-1.5 text-[11px] font-semibold ${
                  isActive ? 'bg-info-soft text-primary' : 'text-ink-48 hover:bg-pearl hover:text-ink'
                }`
              }
            >
              {item.label}
            </NavLink>
          ))}
        </nav>

        <main className={isFixedWorkspace
          ? 'min-h-0 w-full flex-1 overflow-hidden'
          : 'min-h-0 w-full flex-1 overflow-y-auto bg-parchment px-4 py-6 sm:px-6 lg:px-8 lg:py-8'}>
          <Outlet />
        </main>
      </div>
    </div>
  )
}
