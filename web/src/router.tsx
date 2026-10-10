// SPDX-License-Identifier: Apache-2.0

import { createRootRoute, createRoute, createRouter } from '@tanstack/react-router'
import { HomeRedirect, LoginPage, SetupPage } from '@/routes/Auth'
import { NotFoundPage } from '@/routes/ErrorPages'
import { AppShell, RootLayout } from '@/routes/Layout'
import { NewServicePage } from '@/routes/NewService'
import { NodesPage } from '@/routes/Nodes'
import { ProfilePage } from '@/routes/Profile'
import { ProjectPage } from '@/routes/ProjectPage'
import { DashboardPage, ProjectsPage } from '@/routes/Projects'
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
    nodesRoute,
    profileRoute,
    projectRoute,
    environmentRoute,
    newServiceRoute,
    serviceRoute,
    serviceTabRoute,
  ]),
])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
