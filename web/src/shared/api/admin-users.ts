import { request } from './client'
import type {
  AdminUser,
  AdminUserListData,
  AdminUserListQuery,
  CreateAdminUserInput,
  UpdateAdminUserInput,
} from './m1-types'

export function listAdminUsers(query: AdminUserListQuery = {}): Promise<AdminUserListData> {
  const params = new URLSearchParams()
  if (query.status) params.set('status', query.status)
  if (query.role) params.set('role', query.role)
  if (query.page !== undefined) params.set('page', String(query.page))
  if (query.pageSize !== undefined) params.set('pageSize', String(query.pageSize))
  const suffix = params.toString()
  return request<AdminUserListData>(`/api/v1/admin/users${suffix ? `?${suffix}` : ''}`)
}

export function createAdminUser(input: CreateAdminUserInput): Promise<AdminUser> {
  return request<AdminUser>('/api/v1/admin/users', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function updateAdminUser(userId: string, patch: UpdateAdminUserInput): Promise<{ updated: boolean }> {
  return request<{ updated: boolean }>(`/api/v1/admin/users/${userId}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export function resetAdminUserPassword(
  userId: string,
  temporaryPassword: string,
): Promise<{ reset: boolean }> {
  return request<{ reset: boolean }>(`/api/v1/admin/users/${userId}/reset-password`, {
    method: 'POST',
    body: JSON.stringify({ temporaryPassword }),
  })
}
