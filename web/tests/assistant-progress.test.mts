import assert from 'node:assert/strict'
import test from 'node:test'

import { assistantProgressLabel } from '../src/features/assistant/assistant-progress.ts'

test('running turn explains the work before tools or answer tokens appear', () => {
  assert.equal(
    assistantProgressLabel({ phase: 'running', activities: [], hasOutput: false }),
    '正在理解问题并选择信息来源',
  )
})

test('completed tools explain that the answer is being organized before output starts', () => {
  assert.equal(
    assistantProgressLabel({
      phase: 'running',
      activities: [{ displayName: '搜索企业知识库', status: 'succeeded' }],
      hasOutput: false,
    }),
    '资料已返回，正在组织回答',
  )
})

test('retry state explains why the user is still waiting', () => {
  assert.equal(
    assistantProgressLabel({ phase: 'retry', activities: [], hasOutput: false }),
    '回答校验未通过，正在自动重试',
  )
})

test('running tool becomes the current public decision step', () => {
  assert.equal(
    assistantProgressLabel({
      phase: 'running',
      activities: [{ displayName: '搜索企业知识库', status: 'running' }],
      hasOutput: false,
    }),
    '正在搜索企业知识库',
  )
})

test('answer output changes the current step from planning to generating', () => {
  assert.equal(
    assistantProgressLabel({ phase: 'running', activities: [], hasOutput: true }),
    '正在生成回答',
  )
})

test('operational phases use plain-language progress labels', () => {
  const expected = new Map([
    ['submitting', '正在提交问题'],
    ['queued', '问题已收到，正在等待处理'],
    ['reconnecting', '连接波动，正在恢复处理进度'],
    ['finalizing', '回答已生成，正在整理引用'],
    ['failed', '处理未完成'],
  ])

  for (const [phase, label] of expected) {
    assert.equal(
      assistantProgressLabel({ phase, activities: [], hasOutput: false }),
      label,
    )
  }
})
