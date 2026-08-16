import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/app/auth'
import * as api from '@/shared/api'
import type { AdminUser } from '@/shared/api/m1-types'
import { fmtDateTime } from '@/shared/lib/fmt'
import { Badge } from '@/shared/ui/Badge'
import { Button } from '@/shared/ui/Button'
import { FilterChips } from '@/shared/ui/Chips'
import { ConfirmDialog, Dialog } from '@/shared/ui/Dialog'
import { DataTable, type Column } from '@/shared/ui/DataTable'
import { EmptyState } from '@/shared/ui/EmptyState'
import { FieldLabel, Select, TextInput } from '@/shared/ui/Field'
import { PageHeader } from '@/shared/ui/PageHeader'
import { useToast } from '@/shared/ui/Toast'

type Role = AdminUser['role']
type UserStatus = AdminUser['status']
type RowAction =
  | { kind: 'status'; user: AdminUser }
  | { kind: 'role'; user: AdminUser }
  | { kind: 'reset'; user: AdminUser }
  | null

const PAGE_SIZE = 20
const statusOptions = [
  { value: 'all', label: '全部状态' },
  { value: 'active', label: '启用' },
  { value: 'disabled', label: '禁用' },
]
const roleOptions = [
  { value: 'all', label: '全部角色' },
  { value: 'analyst', label: '分析员' },
  { value: 'admin', label: '管理员' },
]

const passwordIsValid = (password: string) => password.length >= 8 && password.length <= 256

export function UsersPage() {
  const qc = useQueryClient()
  const toast = useToast()
  const { user: me } = useAuth()
  const [status, setStatus] = useState<'all' | UserStatus>('all')
  const [role, setRoleFilter] = useState<'all' | Role>('all')
  const [page, setPage] = useState(1)
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

  const usersQuery = useQuery({
    queryKey: ['admin-users', status, role, page],
    queryFn: () =>
      api.listAdminUsers({
        status: status === 'all' ? undefined : status,
        role: role === 'all' ? undefined : role,
        page,
        pageSize: PAGE_SIZE,
      }),
    refetchInterval: false,
  })

  useEffect(() => {
    if (usersQuery.error) toast.error(usersQuery.error.message)
  }, [usersQuery.error, toast])

  const users = usersQuery.data?.items ?? []
  const total = usersQuery.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin-users'] })
  const onError = (error: unknown) =>
    toast.error(error instanceof Error ? error.message : '操作失败')

  const create = useMutation({
    mutationFn: () => api.createAdminUser(form),
    onSuccess: (user) => {
      setCreateOpen(false)
      setForm({ username: '', displayName: '', role: 'analyst', temporaryPassword: '' })
      toast.success(`已创建 ${user.username}，首次登录须修改密码`)
      refresh()
    },
    onError,
  })
  const updateStatus = useMutation({
    mutationFn: (input: { userId: string; status: UserStatus }) =>
      api.updateAdminUser(input.userId, { status: input.status }),
    onSuccess: (_data, input) => {
      setAction(null)
      toast.success(input.status === 'disabled' ? '已禁用该用户' : '已启用该用户')
      refresh()
    },
    onError,
  })
  const updateRole = useMutation({
    mutationFn: (input: { userId: string; role: Role }) =>
      api.updateAdminUser(input.userId, { role: input.role }),
    onSuccess: () => {
      setAction(null)
      toast.success('角色已更新，该用户全部会话已撤销')
      refresh()
    },
    onError,
  })
  const resetPassword = useMutation({
    mutationFn: (input: { userId: string; temporaryPassword: string }) =>
      api.resetAdminUserPassword(input.userId, input.temporaryPassword),
    onSuccess: () => {
      setAction(null)
      setTempPassword('')
      toast.success('密码已重置，该用户已退出登录且下次登录须改密')
      refresh()
    },
    onError,
  })

  const columns: Column<AdminUser>[] = [
    {
      key: 'username',
      title: '用户名',
      render: (user) => (
        <span className={user.status === 'disabled' ? 'opacity-60' : undefined}>
          <span className="font-semibold">{user.username}</span>
          {user.id === me?.id && <span className="ml-1.5 text-[11px] text-ink-48">（自己）</span>}
        </span>
      ),
    },
    {
      key: 'displayName',
      title: '显示名',
      render: (user) => <span className={user.status === 'disabled' ? 'opacity-60' : undefined}>{user.displayName}</span>,
    },
    {
      key: 'role',
      title: '角色',
      render: (user) => <Badge tone={user.role === 'admin' ? 'blue' : 'gray'}>{user.role === 'admin' ? '管理员' : '分析员'}</Badge>,
    },
    {
      key: 'status',
      title: '状态',
      render: (user) => <Badge tone={user.status === 'active' ? 'green' : 'red'}>{user.status === 'active' ? '启用' : '禁用'}</Badge>,
    },
    {
      key: 'mustChangePassword',
      title: '必须改密',
      render: (user) => user.mustChangePassword ? <Badge tone="orange">是</Badge> : <span className="text-ink-48">否</span>,
    },
    {
      key: 'lastLoginAt',
      title: '最近登录',
      className: 'text-ink-48',
      render: (user) => fmtDateTime(user.lastLoginAt),
    },
    {
      key: 'createdAt',
      title: '创建时间',
      className: 'text-ink-48',
      render: (user) => fmtDateTime(user.createdAt),
    },
    {
      key: 'actions',
      title: '操作',
      render: (user) => {
        const isSelf = user.id === me?.id
        const selfHint = isSelf ? '不能操作自己的账号' : undefined
        return (
          <div className="flex items-center gap-1" title={selfHint}>
            <Button variant="neutral" size="sm" disabled={isSelf} onClick={() => setAction({ kind: 'status', user })}>
              {user.status === 'active' ? '禁用' : '启用'}
            </Button>
            <Button
              variant="neutral"
              size="sm"
              disabled={isSelf}
              onClick={() => {
                setNewRole(user.role === 'admin' ? 'analyst' : 'admin')
                setAction({ kind: 'role', user })
              }}
            >
              改角色
            </Button>
            <Button
              variant="neutral"
              size="sm"
              disabled={isSelf}
              onClick={() => {
                setTempPassword('')
                setAction({ kind: 'reset', user })
              }}
            >
              重置密码
            </Button>
          </div>
        )
      },
    },
  ]

  const createValid =
    form.username.trim().length > 0 &&
    form.displayName.trim().length > 0 &&
    passwordIsValid(form.temporaryPassword)

  return (
    <div>
      <PageHeader
        title="用户管理"
        subtitle="创建账号、管理角色与账号状态"
        actions={<Button onClick={() => setCreateOpen(true)}>创建用户</Button>}
      />

      <div className="mb-5 flex flex-wrap items-center gap-4">
        <FilterChips options={statusOptions} value={status} onChange={(value) => { setStatus(value as 'all' | UserStatus); setPage(1) }} />
        <FilterChips options={roleOptions} value={role} onChange={(value) => { setRoleFilter(value as 'all' | Role); setPage(1) }} />
      </div>

      {usersQuery.isError && users.length === 0 ? (
        <EmptyState title="用户列表加载失败" description={usersQuery.error.message} action={<Button variant="neutral" onClick={() => usersQuery.refetch()}>重试</Button>} />
      ) : (
        <>
          <DataTable
            columns={columns}
            rows={users}
            rowKey={(user) => user.id}
            loading={usersQuery.isPending || usersQuery.isFetching}
            emptyText={status === 'all' && role === 'all' ? '还没有用户' : '当前筛选下没有用户'}
          />
          {total > PAGE_SIZE && (
            <div className="mt-4 flex items-center justify-between text-[12px] text-ink-48">
              <span>共 {total} 条 · 第 {page} / {totalPages} 页</span>
              <div className="flex items-center gap-2">
                <Button variant="neutral" size="sm" disabled={page <= 1 || usersQuery.isFetching} onClick={() => setPage((value) => value - 1)}>上一页</Button>
                <Button variant="neutral" size="sm" disabled={page >= totalPages || usersQuery.isFetching} onClick={() => setPage((value) => value + 1)}>下一页</Button>
              </div>
            </div>
          )}
        </>
      )}

      <Dialog
        open={createOpen}
        title="创建用户"
        onClose={() => setCreateOpen(false)}
        footer={
          <>
            <Button variant="neutral" onClick={() => setCreateOpen(false)}>取消</Button>
            <Button onClick={() => create.mutate()} disabled={create.isPending || !createValid}>{create.isPending ? '创建中…' : '创建'}</Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <div>
            <FieldLabel>用户名</FieldLabel>
            <TextInput value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} placeholder="analyst04" />
          </div>
          <div>
            <FieldLabel>显示名</FieldLabel>
            <TextInput value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })} />
          </div>
          <div>
            <FieldLabel>角色</FieldLabel>
            <Select value={form.role} onValueChange={(value) => setForm({ ...form, role: value as Role })}>
              <option value="analyst">分析员</option>
              <option value="admin">管理员</option>
            </Select>
          </div>
          <div>
            <FieldLabel hint="8–256 个字符">临时密码</FieldLabel>
            <TextInput type="password" value={form.temporaryPassword} onChange={(event) => setForm({ ...form, temporaryPassword: event.target.value })} autoComplete="new-password" maxLength={256} />
          </div>
          <p className="text-[12px] leading-[1.6] text-ink-48">临时密码仅本次有效，首次登录须修改。</p>
        </div>
      </Dialog>

      <ConfirmDialog
        open={action?.kind === 'status'}
        title={action?.user.status === 'active' ? '禁用用户' : '启用用户'}
        message={action?.user.status === 'active' ? `确认禁用 ${action.user.username}？` : `确认启用 ${action?.user.username}？`}
        confirmLabel={action?.user.status === 'active' ? '禁用' : '启用'}
        danger={action?.user.status === 'active'}
        busy={updateStatus.isPending}
        onConfirm={() => action && updateStatus.mutate({ userId: action.user.id, status: action.user.status === 'active' ? 'disabled' : 'active' })}
        onCancel={() => setAction(null)}
      />

      <Dialog
        open={action?.kind === 'role'}
        title={`修改角色：${action?.user.username ?? ''}`}
        onClose={() => setAction(null)}
        footer={
          <>
            <Button variant="neutral" onClick={() => setAction(null)}>取消</Button>
            <Button onClick={() => action && updateRole.mutate({ userId: action.user.id, role: newRole })} disabled={updateRole.isPending || newRole === action?.user.role}>{updateRole.isPending ? '提交中…' : '确认修改'}</Button>
          </>
        }
      >
        <FieldLabel>新角色</FieldLabel>
        <Select value={newRole} onValueChange={(value) => setNewRole(value as Role)}>
          <option value="analyst">分析员</option>
          <option value="admin">管理员</option>
        </Select>
        <p className="mt-3 text-[12px] leading-[1.6] text-ink-48">修改角色会撤销该用户全部会话，确认后该用户将退出登录。</p>
      </Dialog>

      <Dialog
        open={action?.kind === 'reset'}
        title={`重置密码：${action?.user.username ?? ''}`}
        onClose={() => setAction(null)}
        footer={
          <>
            <Button variant="neutral" onClick={() => setAction(null)}>取消</Button>
            <Button onClick={() => action && resetPassword.mutate({ userId: action.user.id, temporaryPassword: tempPassword })} disabled={resetPassword.isPending || !passwordIsValid(tempPassword)}>{resetPassword.isPending ? '提交中…' : '确认重置'}</Button>
          </>
        }
      >
        <FieldLabel hint="8–256 个字符">新的临时密码</FieldLabel>
        <TextInput type="password" value={tempPassword} onChange={(event) => setTempPassword(event.target.value)} autoComplete="new-password" maxLength={256} />
        <p className="mt-3 text-[12px] leading-[1.6] text-ink-48">重置后该用户将退出登录，并强制在下次登录时修改密码。</p>
      </Dialog>
    </div>
  )
}
