// API 边界：认证使用当前真实后端，其余尚未落地的业务接口继续使用 Mock。
// 后端每完成一个业务域，就在这里将对应导出切换到真实适配器。
export * from '@/mocks/api'
export * from './types'
export * from './errors'
export { onUnauthorized } from './client'
export { changePassword, login, logout, me } from './auth'
