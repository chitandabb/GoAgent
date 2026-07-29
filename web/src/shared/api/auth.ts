import type { CurrentUser } from './types'
import { request, setCSRFToken } from './client'
import { ApiError } from './errors'

interface AuthResponse {
  user: CurrentUser
  csrfToken: string
  idleExpiresAt: string
  absoluteExpiresAt: string
}

export async function login(username: string, password: string): Promise<CurrentUser> {
  const result = await request<AuthResponse>('/api/v1/auth/login', {
    method: 'POST',
    body: JSON.stringify({ username: username.trim(), password }),
  })
  setCSRFToken(result.csrfToken)
  return result.user
}

export async function me(): Promise<CurrentUser | null> {
  try {
    const result = await request<AuthResponse>('/api/v1/auth/me', {
      ignoreUnauthorized: true,
    })
    setCSRFToken(result.csrfToken)
    return result.user
  } catch (error) {
    setCSRFToken(null)
    if (error instanceof ApiError && error.status === 401) return null
    throw error
  }
}

export async function logout(): Promise<void> {
  try {
    await request<{ loggedOut: boolean }>('/api/v1/auth/logout', { method: 'POST' })
  } finally {
    setCSRFToken(null)
  }
}

export async function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<void> {
  try {
    await request<unknown>('/api/v1/auth/change-password', {
      method: 'POST',
      body: JSON.stringify({ currentPassword, newPassword }),
    })
    setCSRFToken(null)
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      throw new ApiError(error.code, '后端暂未提供修改密码接口', error.status, error.requestId)
    }
    throw error
  }
}
