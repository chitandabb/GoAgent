import { createBrowserRouter, Navigate } from 'react-router'
import { RequireAdmin, RequireAuth } from './auth'
import { AdminLayout } from './layouts/AdminLayout'
import { NotFoundPage } from './NotFoundPage'
import { WorkbenchLayout } from './layouts/WorkbenchLayout'
import { ChangePasswordPage } from '@/features/auth/ChangePasswordPage'
import { LoginPage } from '@/features/auth/LoginPage'
import { CaseDetailPage } from '@/features/cases/CaseDetailPage'
import { CasesPage } from '@/features/cases/CasesPage'
import { TaskDetailPage } from '@/features/diagnosis/TaskDetailPage'
import { TasksPage } from '@/features/diagnosis/TasksPage'
import { KnowledgePage } from '@/features/knowledge/KnowledgePage'
import { ReportPage } from '@/features/reports/ReportPage'
import { DataSourcesPage } from '@/features/admin/DataSourcesPage'
import { SystemPage } from '@/features/admin/SystemPage'
import { UsersPage } from '@/features/admin/UsersPage'
import { CaseWorkbenchRedirect } from '@/features/workbench/CaseWorkbenchRedirect'
import { WorkbenchPage } from '@/features/workbench/WorkbenchPage'
import { AssistantPage } from '@/features/assistant/AssistantPage'

export const router = createBrowserRouter([
  { path: '/login', element: <LoginPage /> },
  {
    element: <RequireAuth />,
    children: [
      { path: '/change-password', element: <ChangePasswordPage /> },
      {
        element: <WorkbenchLayout />,
        children: [
          { path: '/', element: <Navigate to="/workbench" replace /> },
          { path: '/workbench', element: <WorkbenchPage /> },
          { path: '/workbench/:workspaceId', element: <WorkbenchPage /> },
          { path: '/cases', element: <CasesPage /> },
          { path: '/cases/:caseId', element: <CaseDetailPage /> },
          { path: '/cases/:caseId/diagnose', element: <CaseWorkbenchRedirect /> },
          { path: '/tasks', element: <TasksPage /> },
          { path: '/tasks/:taskId', element: <TaskDetailPage /> },
          { path: '/tasks/:taskId/report', element: <ReportPage /> },
          { path: '/assistant', element: <AssistantPage /> },
          { path: '/knowledge', element: <KnowledgePage /> },
          {
            element: <RequireAdmin />,
            children: [
              {
                path: '/admin',
                element: <AdminLayout />,
                children: [
                  { index: true, element: <Navigate to="/admin/users" replace /> },
                  { path: 'users', element: <UsersPage /> },
                  { path: 'data-sources', element: <DataSourcesPage /> },
                  { path: 'system', element: <SystemPage /> },
                ],
              },
            ],
          },
          { path: '*', element: <NotFoundPage /> },
        ],
      },
    ],
  },
])
