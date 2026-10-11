// SPDX-License-Identifier: Apache-2.0

import { createRootRoute, createRoute, createRouter } from '@tanstack/react-router'
import {
  ForgotPasswordPage,
  HomeRedirect,
  InvitePage,
  LoginPage,
  ResetPasswordPage,
  SetupPage,
} from '@/routes/Auth'
import { ConfigAsCodePage } from '@/routes/ConfigAsCode'
import { AuditPage } from '@/routes/Audit'
import { AlertSettingsPage } from '@/routes/AlertSettings'
import { AlertsPage } from '@/routes/AlertsPage'
import { LogsPage, validateLogsSearch } from '@/routes/LogsPage'
import { OrgSettingsPage } from '@/routes/OrgSettings'
import { SsoPage, validateSsoSearch } from '@/routes/Sso'
import { MembersPage } from '@/routes/Members'
import { ProjectRolesPage } from '@/routes/ProjectRoles'
import { TeamsPage } from '@/routes/Teams'
import { TokensPage } from '@/routes/Tokens'
import { NotFoundPage } from '@/routes/ErrorPages'
import { AppShell, RootLayout } from '@/routes/Layout'
import { DeploymentPage } from '@/routes/DeploymentPage'
import { DeploymentsPage } from '@/routes/DeploymentsPage'
import { NewServicePage } from '@/routes/NewService'
import type { StartPath } from '@/routes/NewService'
import { BackupsPage } from '@/routes/BackupsPage'
import { BackupTargetsPage } from '@/routes/BackupTargets'
import { CatalogPage } from '@/routes/Catalog'
import { NodesPage } from '@/routes/Nodes'
import { NodeDetailPage } from '@/routes/NodeDetail'
import { NodeHistoryPage } from '@/routes/NodeHistory'
import { PreviewDetailPage } from '@/routes/PreviewDetail'
import { PreviewsPage } from '@/routes/Previews'
import { ProfilePage } from '@/routes/Profile'
import { ProjectPage } from '@/routes/ProjectPage'
import { DashboardPage, ProjectsPage } from '@/routes/Projects'
import { ReleaseDiffPage } from '@/routes/ReleaseDiffPage'
import { ServicePage } from '@/routes/ServicePage'

const rootRoute = createRootRoute({ component: RootLayout, notFoundComponent: NotFoundPage })

const homeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: HomeRedirect,
})
const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: LoginPage,
  validateSearch: (search: Record<string, unknown>): { expired?: boolean; sso_error?: string } => ({
    ...(search.expired ? { expired: true } : {}),
    ...(typeof search.sso_error === 'string' ? { sso_error: search.sso_error } : {}),
  }),
})
const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/setup',
  component: SetupPage,
})
const inviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/invite',
  component: InvitePage,
})
const resetPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/reset-password',
  component: ResetPasswordPage,
})
const forgotPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forgot-password',
  component: ForgotPasswordPage,
})

// Every signed-in page lives under /o/:org (navigation, section 2).
const orgRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/o/$org',
  component: AppShell,
})
const dashboardRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/',
  component: DashboardPage,
})
const projectsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'projects',
  component: ProjectsPage,
})
const nodesRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'nodes',
  component: NodesPage,
})
const nodeHistoryRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'nodes/history',
  component: NodeHistoryPage,
})
const nodeRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'nodes/$node',
  component: NodeDetailPage,
})
const deploymentsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'deployments',
  component: DeploymentsPage,
  // The filters stay in the URL, so a link restores the view (navigation, section 2).
  validateSearch: (
    search: Record<string, unknown>,
  ): { project?: string; env?: string; state?: string; service?: string } => ({
    ...(typeof search.project === 'string' && search.project ? { project: search.project } : {}),
    ...(typeof search.env === 'string' && search.env ? { env: search.env } : {}),
    ...(typeof search.state === 'string' && search.state ? { state: search.state } : {}),
    ...(typeof search.service === 'string' && search.service ? { service: search.service } : {}),
  }),
})
const previewsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'previews',
  component: PreviewsPage,
  // The project filter stays in the URL (UI-18).
  validateSearch: (search: Record<string, unknown>): { project?: string } =>
    typeof search.project === 'string' && search.project ? { project: search.project } : {},
})
const previewDetailRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/previews/$env',
  component: PreviewDetailPage,
})
const catalogRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'catalog',
  component: CatalogPage,
})
const backupsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'backups',
  component: BackupsPage,
})
const backupTargetsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/backup-targets',
  component: BackupTargetsPage,
})
const alertsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'alerts',
  component: AlertsPage,
})
const alertSettingsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/alerts',
  component: AlertSettingsPage,
})
const logsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'logs',
  component: LogsPage,
  // The filters stay in the URL, so a link restores the search (UI-37).
  validateSearch: validateLogsSearch,
})
const orgSettingsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/organization',
  component: OrgSettingsPage,
})
const ssoRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/sso',
  component: SsoPage,
  // "Test login" comes back to this page with its result in the address.
  validateSearch: validateSsoSearch,
})
const profileRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/profile',
  component: ProfilePage,
})
const membersRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/members',
  component: MembersPage,
})
const teamsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/teams',
  component: TeamsPage,
})
const tokensRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/tokens',
  component: TokensPage,
})
const auditRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/audit',
  component: AuditPage,
})
const projectRolesRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/roles',
  component: ProjectRolesPage,
})
const projectConfigRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/config',
  component: ConfigAsCodePage,
})
const projectRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project',
  component: ProjectPage,
})
const environmentRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/e/$env',
  component: ProjectPage,
})
const newServiceRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/e/$env/new',
  component: NewServicePage,
  // The chosen start path of the wizard stays in the URL (UI-32).
  validateSearch: (search: Record<string, unknown>): { source?: StartPath } =>
    search.source === 'git' ||
    search.source === 'image' ||
    search.source === 'template' ||
    search.source === 'database' ||
    search.source === 'compose'
      ? { source: search.source }
      : {},
})
const deploymentRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/e/$env/s/$service/deployments/$deployment',
  component: DeploymentPage,
})
const releaseDiffRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/e/$env/s/$service/releases/$release/diff/$other',
  component: ReleaseDiffPage,
})
const serviceRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/e/$env/s/$service',
  component: ServicePage,
})
const serviceTabRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'p/$project/e/$env/s/$service/$tab',
  component: ServicePage,
})

const routeTree = rootRoute.addChildren([
  homeRoute,
  loginRoute,
  setupRoute,
  inviteRoute,
  resetPasswordRoute,
  forgotPasswordRoute,
  orgRoute.addChildren([
    dashboardRoute,
    projectsRoute,
    deploymentsRoute,
    nodesRoute,
    nodeHistoryRoute,
    nodeRoute,
    previewsRoute,
    previewDetailRoute,
    catalogRoute,
    backupsRoute,
    backupTargetsRoute,
    alertsRoute,
    alertSettingsRoute,
    logsRoute,
    orgSettingsRoute,
    ssoRoute,
    profileRoute,
    membersRoute,
    teamsRoute,
    tokensRoute,
    auditRoute,
    projectRolesRoute,
    projectConfigRoute,
    projectRoute,
    environmentRoute,
    newServiceRoute,
    serviceRoute,
    serviceTabRoute,
    deploymentRoute,
    releaseDiffRoute,
  ]),
])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
