import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import { Button } from '@/shared/ui/Button'
import { FieldLabel, TextInput } from '@/shared/ui/Field'
import { useToast } from '@/shared/ui/Toast'
import { Wordmark } from '@/shared/ui/Wordmark'

export function ChangePasswordPage() {
  const { user, logout, clearUser } = useAuth()
  const navigate = useNavigate()
  const toast = useToast()
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (newPassword !== confirmPassword) {
      setError('两次输入的新密码不一致')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.changePassword(currentPassword, newPassword)
      // api.md：改密成功撤销全部旧 Session，需重新登录
      clearUser()
      toast.success('密码已修改，请使用新密码重新登录')
      navigate('/login', { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : '修改失败，请重试')
    } finally {
      setBusy(false)
    }
  }

  const handleLogout = async () => {
    await logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-parchment px-6">
      <div className="mb-10 text-center">
        <h1 className="text-[40px] leading-[1.1]">
          <Wordmark className="tracking-[-0.4px]" />
        </h1>
        <p className="mt-3 text-[17px] text-ink-48">修改密码</p>
      </div>

      <form
        onSubmit={handleSubmit}
        className="w-full max-w-[380px] rounded-card border border-hairline bg-canvas p-8"
      >
        {user?.mustChangePassword && (
          <div className="mb-5 rounded-capsule bg-warn-soft px-4 py-3 text-[12px] leading-[1.7] text-warn">
            当前使用临时密码，须先设置新密码才能继续使用系统。
          </div>
        )}
        <div className="mb-5">
          <FieldLabel>当前密码</FieldLabel>
          <TextInput
            type="password"
            value={currentPassword}
            onChange={(e) => setCurrentPassword(e.target.value)}
            autoComplete="current-password"
            autoFocus
          />
        </div>
        <div className="mb-5">
          <FieldLabel hint="至少 8 位">新密码</FieldLabel>
          <TextInput
            type="password"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            autoComplete="new-password"
          />
        </div>
        <div className="mb-6">
          <FieldLabel>确认新密码</FieldLabel>
          <TextInput
            type="password"
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            autoComplete="new-password"
          />
        </div>
        {error && <p className="mb-4 text-[13px] text-danger">{error}</p>}
        <Button
          type="submit"
          className="w-full"
          disabled={busy || !currentPassword || !newPassword || !confirmPassword}
        >
          {busy ? '提交中…' : '修改密码'}
        </Button>
        <button
          type="button"
          onClick={handleLogout}
          className="press mt-4 block w-full text-center text-[13px] text-primary"
        >
          退出登录
        </button>
      </form>
    </div>
  )
}
