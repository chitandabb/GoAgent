import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type { AdminUser, Role } from '@/shared/api'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { ConfirmDialog, Dialog } from '@/shared/ui/Dialog'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { FieldLabel, Select, TextInput } from '@/shared/ui/Field'
import { useToast } from '@/shared/ui/Toast'

type RowAction =
  | { kind: 'status'; user: AdminUser }
  | { kind: 'role'; user: AdminUser }
  | { kind: 'reset'; user: AdminUser }
  | null

export function UsersPage() {
  const qc = useQueryClient()
  const toast = useToast()
  const { user: me } = useAuth()
  const users = useQuery({ queryKey: ['admin-users'], queryFn: api.listUsers })

  const [createOpen, setCreateOpen] = useState(false)
  const [form, setForm] = useState({
    username: '',
    displayName: '',
    role: 'analyst' as Role,
    temporaryPassword: '',
  })
  const [action, setAction] = useState<RowAction>(null)
  const [newRole, setNewRole] = useState<Role>('analyst')
  const [tempPassword, setTempPassword] = useState('')

  const refresh = () => qc.invalidateQueries({ queryKey: ['admin-users'] })
  const onError = (e: unknown) =>
    toast.error(e instanceof Error ? e.message : '操作失败')

  const create = useMutation({
    mutationFn: () => api.createUser(form),
    onSuccess: (u) => {
      setCreateOpen(false)
      setForm({ username: '', displayName: '', role: 'analyst', temporaryPassword: '' })
      toast.success(`已创建 ${u.username}，首次登录须修改密码`)
      refresh()
    },
    onError,
  })
  const setStatus = useMutation({
    mutationFn: (p: { userId: string; status: 'active' | 'disabled' }) =>
      api.setUserStatus(p.userId, p.status),
    onSuccess: (_d, p) => {
      setAction(null)
      toast.success(
        p.status === 'disabled' ? '已禁用该用户并撤销其全部会话' : '已启用该用户',
      )
      refresh()
    },
    onError,
  })
  const setRole = useMutation({
    mutationFn: (p: { userId: string; role: Role }) => api.setUserRole(p.userId, p.role),
    onSuccess: () => {
      setAction(null)
      toast.success('角色已更新，目标用户全部会话已撤销')
      refresh()
    },
    onError,
  })
  const reset = useMutation({
    mutationFn: (p: { userId: string; temporaryPassword: string }) =>
      api.resetUserPassword(p.userId, p.temporaryPassword),
    onSuccess: () => {
      setAction(null)
      setTempPassword('')
      toast.success('已重置临时密码，该用户下次登录须改密')
      refresh()
    },
    onError,
  })

  const columns: Column<AdminUser>[] = [
    {
      key: 'username',
      title: '用户名',
      render: (u) => (
        <span>
          <span className="font-semibold">{u.username}</span>
          {u.id === me?.id && (
            <span className="ml-1.5 text-[11px] text-ink-48">（自己）</span>
          )}
        </span>
      ),
    },
    { key: 'displayName', title: '姓名', render: (u) => u.displayName },
    {
      key: 'role',
      title: '角色',
      render: (u) => (
        <Badge tone={u.role === 'admin' ? 'blue' : 'gray'}>{u.role}</Badge>
      ),
    },
    {
      key: 'status',
      title: '状态',
      render: (u) => (
        <span className="flex items-center gap-1.5">
          <Badge tone={u.status === 'active' ? 'green' : 'red'}>
            {u.status === 'active' ? '启用' : '禁用'}
          </Badge>
          {u.mustChangePassword && <Badge tone="orange">待改密</Badge>}
        </span>
      ),
    },
    {
      key: 'lastLoginAt',
      title: '最近登录',
      className: 'text-ink-48',
      render: (u) => fmtDateTime(u.lastLoginAt),
    },
    {
      key: 'actions',
      title: '操作',
      render: (u) => (
        <div className="flex items-center gap-1">
          <Button
            variant="neutral"
            size="sm"
            onClick={() => setAction({ kind: 'status', user: u })}
          >
            {u.status === 'active' ? '禁用' : '启用'}
          </Button>
          <Button
            variant="neutral"
            size="sm"
            onClick={() => {
              setNewRole(u.role === 'admin' ? 'analyst' : 'admin')
              setAction({ kind: 'role', user: u })
            }}
          >
            改角色
          </Button>
          <Button
            variant="neutral"
            size="sm"
            onClick={() => {
              setTempPassword('')
              setAction({ kind: 'reset', user: u })
            }}
          >
            重置密码
          </Button>
        </div>
      ),
    },
  ]

  return (
    <div>
      <div className="mb-4 flex items-center justify-between gap-4">
        <p className="text-[12px] text-ink-48">
          演示限制：新建用户仅进入用户列表，不接入登录账号池。
        </p>
        <Button onClick={() => setCreateOpen(true)}>创建用户</Button>
      </div>

      <DataTable
        columns={columns}
        rows={users.data ?? []}
        rowKey={(u) => u.id}
        loading={users.isPending}
      />

      {/* 创建用户 */}
      <Dialog
        open={createOpen}
        title="创建用户"
        onClose={() => setCreateOpen(false)}
        footer={
          <>
            <Button variant="neutral" onClick={() => setCreateOpen(false)}>
              取消
            </Button>
            <Button onClick={() => create.mutate()} disabled={create.isPending}>
              {create.isPending ? '创建中…' : '创建'}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <div>
            <FieldLabel>用户名</FieldLabel>
            <TextInput
              value={form.username}
              onChange={(e) => setForm({ ...form, username: e.target.value })}
              placeholder="analyst04"
            />
          </div>
          <div>
            <FieldLabel>姓名</FieldLabel>
            <TextInput
              value={form.displayName}
              onChange={(e) => setForm({ ...form, displayName: e.target.value })}
            />
          </div>
          <div>
            <FieldLabel>角色</FieldLabel>
            <Select
              value={form.role}
              onValueChange={(role) => setForm({ ...form, role: role as Role })}
            >
              <option value="analyst">analyst</option>
              <option value="admin">admin</option>
            </Select>
          </div>
          <div>
            <FieldLabel hint="至少 8 位，响应不回显">临时密码</FieldLabel>
            <TextInput
              type="password"
              value={form.temporaryPassword}
              onChange={(e) => setForm({ ...form, temporaryPassword: e.target.value })}
              autoComplete="new-password"
            />
          </div>
          <p className="text-[12px] leading-[1.6] text-ink-48">
            新用户 mustChangePassword=true，首次登录须修改密码；禁止匿名注册。
          </p>
        </div>
      </Dialog>

      {/* 启用/禁用 */}
      <ConfirmDialog
        open={action?.kind === 'status'}
        title={action?.user.status === 'active' ? '禁用用户' : '启用用户'}
        message={
          action?.user.status === 'active'
            ? `禁用 ${action.user.username} 后，其全部会话将被撤销，历史任务与审计记录保留。`
            : `确认启用 ${action?.user.username}?`
        }
        confirmLabel={action?.user.status === 'active' ? '禁用' : '启用'}
        danger={action?.user.status === 'active'}
        busy={setStatus.isPending}
        onConfirm={() =>
          action &&
          setStatus.mutate({
            userId: action.user.id,
            status: action.user.status === 'active' ? 'disabled' : 'active',
          })
        }
        onCancel={() => setAction(null)}
      />

      {/* 改角色 */}
      <Dialog
        open={action?.kind === 'role'}
        title={`修改角色：${action?.user.username ?? ''}`}
        onClose={() => setAction(null)}
        footer={
          <>
            <Button variant="neutral" onClick={() => setAction(null)}>
              取消
            </Button>
            <Button
              onClick={() =>
                action && setRole.mutate({ userId: action.user.id, role: newRole })
              }
              disabled={setRole.isPending || newRole === action?.user.role}
            >
              {setRole.isPending ? '提交中…' : '确认修改'}
            </Button>
          </>
        }
      >
        <FieldLabel>新角色</FieldLabel>
        <Select value={newRole} onValueChange={(role) => setNewRole(role as Role)}>
          <option value="analyst">analyst</option>
          <option value="admin">admin</option>
        </Select>
        <p className="mt-3 text-[12px] leading-[1.6] text-ink-48">
          修改后目标用户全部会话被撤销；系统会阻止最后一个可用 admin 被降级。
        </p>
      </Dialog>

      {/* 重置密码 */}
      <Dialog
        open={action?.kind === 'reset'}
        title={`重置密码：${action?.user.username ?? ''}`}
        onClose={() => setAction(null)}
        footer={
          <>
            <Button variant="neutral" onClick={() => setAction(null)}>
              取消
            </Button>
            <Button
              onClick={() =>
                action &&
                reset.mutate({ userId: action.user.id, temporaryPassword: tempPassword })
              }
              disabled={reset.isPending || tempPassword.length < 8}
            >
              {reset.isPending ? '提交中…' : '重置'}
            </Button>
          </>
        }
      >
        <FieldLabel hint="至少 8 位，响应不回显">新的临时密码</FieldLabel>
        <TextInput
          type="password"
          value={tempPassword}
          onChange={(e) => setTempPassword(e.target.value)}
          autoComplete="new-password"
        />
        <p className="mt-3 text-[12px] leading-[1.6] text-ink-48">
          重置后目标用户全部会话被撤销，下次登录须修改密码。
        </p>
      </Dialog>
    </div>
  )
}
