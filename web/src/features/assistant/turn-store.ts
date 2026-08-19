import type { ConversationCaseReference } from '@/shared/api/m1-types'

interface StoredTurnInput {
  content: string
  attachments: { attachmentId: string }[]
  caseReferences: ConversationCaseReference[]
}

interface StoredActiveTurn {
  turnId: string
  input: StoredTurnInput
}

type StoredTurnMap = Record<string, StoredActiveTurn>

const storageKey = 'mesguard.active-conversation-turns.v1'

function readAll(): StoredTurnMap {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(storageKey) ?? '{}')
    if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
    return Object.fromEntries(
      Object.entries(value).filter((entry): entry is [string, StoredActiveTurn] => {
        const item = entry[1]
        if (!item || typeof item !== 'object' || Array.isArray(item)) return false
        const current = item as Partial<StoredActiveTurn>
        return typeof current.turnId === 'string' &&
          !!current.input &&
          typeof current.input.content === 'string' &&
          Array.isArray(current.input.attachments) &&
          Array.isArray(current.input.caseReferences)
      }),
    )
  } catch {
    return {}
  }
}

function writeAll(value: StoredTurnMap): void {
  try {
    sessionStorage.setItem(storageKey, JSON.stringify(value))
  } catch {
    // Turn streaming still works when browser storage is unavailable.
  }
}

export function loadActiveTurn(conversationId: string): StoredActiveTurn | undefined {
  return readAll()[conversationId]
}

export function saveActiveTurn(conversationId: string, value: StoredActiveTurn): void {
  writeAll({ ...readAll(), [conversationId]: value })
}

export function clearActiveTurn(conversationId: string, turnId?: string): void {
  const current = readAll()
  if (!current[conversationId] || (turnId && current[conversationId].turnId !== turnId)) return
  delete current[conversationId]
  writeAll(current)
}
