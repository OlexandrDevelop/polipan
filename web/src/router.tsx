// SPDX-License-Identifier: Apache-2.0

import { createRootRoute, createRoute, createRouter } from '@tanstack/react-router'
import { Layout } from '@/routes/Layout'
import { Overview } from '@/routes/Overview'

const rootRoute = createRootRoute({ component: Layout })
const overviewRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: Overview,
})

const routeTree = rootRoute.addChildren([overviewRoute])

export const router = createRouter({ routeTree })

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
