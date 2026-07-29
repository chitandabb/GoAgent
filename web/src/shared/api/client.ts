import { ApiError, type ApiFieldError } from './errors'

interface ApiEnvelope<T> {
  code: number
  message: string
  data?: T
  requestId?: string
}

interface ErrorData {
  fields?: ApiFieldError[]
}

interface RequestOptions extends RequestInit {
  ignoreUnauthorized?: boolean
}

let csrfToken = ''
const unauthorizedListeners = new Set<() => void>()

export function setCSRFToken(token: string | null): void {
  csrfToken = token ?? ''
}

export function onUnauthorized(listener: () => void): () => void {
  unauthorizedListeners.add(listener)
  return () => unauthorizedListeners.delete(listener)
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { ignoreUnauthorized = false, ...init } = options
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')

  if (init.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method) && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }

  let response: Response
  try {
    response = await fetch(path, {
      ...init,
      method,
      headers,
      credentials: 'same-origin',
    })
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    throw new ApiError(50301, '暂时无法连接服务')
  }

  let envelope: ApiEnvelope<T>
  try {
    envelope = (await response.json()) as ApiEnvelope<T>
  } catch {
    throw new ApiError(
      50000,
      `服务返回了无法解析的响应（HTTP ${response.status}）`,
      response.status,
    )
  }

  if (!response.ok || envelope.code !== 0) {
    if (response.status === 401 && !ignoreUnauthorized) {
      setCSRFToken(null)
      unauthorizedListeners.forEach((listener) => listener())
    }
    const errorData = envelope.data as ErrorData | undefined
    throw new ApiError(
      envelope.code,
      envelope.message || `请求失败（HTTP ${response.status}）`,
      response.status,
      envelope.requestId,
      errorData?.fields ?? [],
    )
  }

  return envelope.data as T
}
