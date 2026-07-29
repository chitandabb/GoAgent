import { useState } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { useAuth } from '@/app/auth'
import { ApiError } from '@/shared/api'
import { Button } from '@/shared/ui/Button'
import { FieldLabel, TextInput } from '@/shared/ui/Field'
import { Wordmark } from '@/shared/ui/Wordmark'

export function LoginPage() {
  const { login } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const from = (location.state as { from?: string } | null)?.from ?? '/cases'

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await login(username, password)
      navigate(from, { replace: true })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '暂时无法连接认证服务')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-parchment px-6">
      <div className="mb-10 text-center">
        <h1 className="text-[40px] leading-[1.1]">
          <Wordmark className="tracking-[-0.4px]" />
        </h1>
        <p className="mt-3 text-[17px] text-ink-48">工单诊断辅助系统</p>
      </div>

      <form
        onSubmit={handleSubmit}
        className="w-full max-w-[380px] rounded-card border border-hairline bg-canvas p-8"
      >
        <div className="mb-5">
          <FieldLabel>用户名</FieldLabel>
          <TextInput
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            autoFocus
          />
        </div>
        <div className="mb-6">
          <FieldLabel>密码</FieldLabel>
          <TextInput
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
          />
        </div>
        {error && <p className="mb-4 text-[13px] text-danger">{error}</p>}
        <Button type="submit" className="w-full" disabled={busy || !username || !password}>
          {busy ? '登录中…' : '登录'}
        </Button>
      </form>

      <p className="mt-10 text-[12px] text-ink-48">本地账号登录 · 会话由服务端安全管理</p>
    </div>
  )
}
