import assert from 'node:assert/strict'
import test from 'node:test'

import {
  collapseBlankConversations,
  latestCreatedTaskIdForCase,
  reusableDraftConversationId,
} from '../src/features/assistant/conversation-draft.ts'

test('new conversation reuses the existing blank conversation', () => {
  const conversations = [
    { id: 'answered', firstUserMessage: '已有问题' },
    { id: 'blank' },
  ]

  assert.equal(reusableDraftConversationId(conversations), 'blank')
})

test('new conversation reuses a just-created blank conversation before the list refreshes', () => {
  assert.equal(reusableDraftConversationId([], 'just-created'), 'just-created')
})

test('conversation list shows only the newest blank conversation', () => {
  const conversations = [
    { id: 'blank-newest' },
    { id: 'answered', firstUserMessage: '已有问题' },
    { id: 'blank-older' },
  ]

  assert.deepEqual(
    collapseBlankConversations(conversations).map((conversation) => conversation.id),
    ['blank-newest', 'answered'],
  )
})

test('dossier keeps the latest created task scoped to the selected case', () => {
  const messages = [
    {
      caseReferences: [{ externalCaseId: 'case-1001', kind: 'selected' }],
      taskReferences: [{ taskId: 'task-1001', kind: 'created' }],
    },
    {
      caseReferences: [{ externalCaseId: 'case-1002', kind: 'selected' }],
      taskReferences: [{ taskId: 'task-1002', kind: 'created' }],
    },
  ]

  assert.equal(latestCreatedTaskIdForCase(messages, 'case-1001'), 'task-1001')
  assert.equal(latestCreatedTaskIdForCase(messages, 'case-1002'), 'task-1002')
  assert.equal(latestCreatedTaskIdForCase(messages, 'case-1003'), undefined)
  assert.equal(latestCreatedTaskIdForCase(messages, null), undefined)
})
