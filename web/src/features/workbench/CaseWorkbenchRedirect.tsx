import { Navigate, useParams, useSearchParams } from 'react-router'
import { openCaseWorkspace } from '@/shared/workspace/workspace-store'

export function CaseWorkbenchRedirect() {
  const { caseId = '' } = useParams()
  const [searchParams] = useSearchParams()
  const workspace = openCaseWorkspace(caseId)
  const retryOf = searchParams.get('retryOf')
  const suffix = retryOf ? `?retryOf=${encodeURIComponent(retryOf)}` : ''
  return <Navigate to={`/workbench/${workspace.workspaceId}${suffix}`} replace />
}
