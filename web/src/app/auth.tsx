import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react'
import { Navigate, Outlet, useLocation } from 'react-router'
import * as api from '@/shared/api'
import type { CurrentUser } from '@/shared/api'
import { PageLoading } from '@/shared/ui/Spinner'

interface AuthState {
  user: CurrentUser | null
  ready: boolean
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
  /** 会话已在服务端撤销时（如改密后）清除本地状态，不再调用 logout 接口 */
  clearUser: () => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<CurrentUser | null>(null)
  const [ready, setReady] = useState(false)

  useEffect(() => {
    let cancelled = false
    // 对应真实实现的 GET /auth/me：刷新后恢复认证状态
    api.me().then((u) => {
      if (!cancelled) {
        setUser(u)
        setReady(true)
      }
    })
    return () => {
      cancelled = true
    }
  }, [])

  const login = useCallback(async (username: string, password: string) => {
    const u = await api.login(username, password)
    setUser(u)
  }, [])

  const logout = useCallback(async () => {
    await api.logout()
    setUser(null)
  }, [])

  const clearUser = useCallback(() => setUser(null), [])

  const value = useMemo(
    () => ({ user, ready, login, logout, clearUser }),
    [user, ready, login, logout, clearUser],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}

export function RequireAuth() {
  const { user, ready } = useAuth()
  const location = useLocation()
  if (!ready) return <PageLoading />
  if (!user) {
    return <Navigate to="/login" state={{ from: location.pathname }} replace />
  }
  // 临时密码账号只允许改密与退出（api.md 认证规则）
  if (user.mustChangePassword && location.pathname !== '/change-password') {
    return <Navigate to="/change-password" replace />
  }
  return <Outlet />
}

export function RequireAdmin() {
  const { user } = useAuth()
  if (user?.role !== 'admin') return <Navigate to="/" replace />
  return <Outlet />
}
