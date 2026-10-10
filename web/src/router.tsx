// SPDX-License-Identifier: Apache-2.0

import { createRootRoute, createRoute, createRouter } from '@tanstack/react-router'
import { HomeRedirect, LoginPage, SetupPage } from '@/routes/Auth'
import { NotFoundPage } from '@/routes/ErrorPages'
import { AppShell, RootLayout } from '@/routes/Layout'
import { DeploymentPage } from '@/routes/DeploymentPage'
import { DeploymentsPage } from '@/routes/DeploymentsPage'
import { NewServicePage } from '@/routes/NewService'
import type { SourceKind } from '@/routes/NewService'
import { NodesPage } from '@/routes/Nodes'
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
  validateSearch: (search: Record<string, unknown>): { expired?: boolean } =>
    search.expired ? { expired: true } : {},
})
const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/setup',
  component: SetupPage,
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
const profileRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: 'settings/profile',
  component: ProfilePage,
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
  validateSearch: (search: Record<string, unknown>): { source?: SourceKind } =>
    search.source === 'git' || search.source === 'image' ? { source: search.source } : {},
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
  orgRoute.addChildren([
    dashboardRoute,
    projectsRoute,
    deploymentsRoute,
    nodesRoute,
    profileRoute,
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
