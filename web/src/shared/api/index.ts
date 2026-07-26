// API 边界：业务组件只允许从这里 import。
//
// 当前转发到 src/mocks（本地模拟实现）。接入真实后端后：
//   1. 删除 src/mocks 目录；
//   2. 把这里替换为基于 fetch 的实现（统一信封解包、CSRF 注入、
//      SSE 用 EventSource + Last-Event-ID);
//   3. 打开 vite.config.ts 中的 /api 代理。
export * from '@/mocks/api'
export * from './types'
