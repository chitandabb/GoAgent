export interface DraftConversationCandidate {
  id: string
  firstUserMessage?: string
}

export interface DossierMessageCandidate {
  caseReferences: readonly { externalCaseId: string; kind: string }[]
  taskReferences: readonly { taskId: string; kind: string }[]
}

/** 空白会话没有用户消息；再次新建时应复用，避免堆积无内容的会话。 */
export function reusableDraftConversationId(
  conversations: readonly DraftConversationCandidate[],
  justCreatedDraftId: string | null = null,
): string | null {
  if (justCreatedDraftId) return justCreatedDraftId
  return conversations.find((conversation) => !conversation.firstUserMessage?.trim())?.id ?? null
}

/** 列表按更新时间倒序；保留第一个空白会话即可，旧空白草稿不再占满列表。 */
export function collapseBlankConversations<T extends DraftConversationCandidate>(
  conversations: readonly T[],
): T[] {
  let blankSeen = false
  return conversations.filter((conversation) => {
    if (conversation.firstUserMessage?.trim()) return true
    if (blankSeen) return false
    blankSeen = true
    return true
  })
}

/** 卷宗只展示当前工单创建的最近任务，避免切换工单时串入上一张工单的处理结果。 */
export function latestCreatedTaskIdForCase(
  messages: readonly DossierMessageCandidate[],
  selectedCaseId: string | null,
): string | undefined {
  if (!selectedCaseId) return undefined

  return [...messages]
    .reverse()
    .filter((message) =>
      message.caseReferences.some(
        (reference) => reference.kind === 'selected' && reference.externalCaseId === selectedCaseId,
      ),
    )
    .flatMap((message) => [...message.taskReferences].reverse())
    .find((reference) => reference.kind === 'created')?.taskId
}
