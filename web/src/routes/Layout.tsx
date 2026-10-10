// SPDX-License-Identifier: Apache-2.0

import { Link, Outlet } from '@tanstack/react-router'
import { ThemeToggle } from '@/components/ThemeToggle'

export function Layout() {
  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <nav
        aria-label="Main"
        className="flex shrink-0 flex-col gap-4 border-b border-border bg-surface p-4 md:w-56 md:border-b-0 md:border-r"
      >
        <span className="text-lg font-semibold">Polipan</span>
        <ul className="flex gap-2 md:flex-col">
          <li>
            <Link
              to="/"
              className="block min-h-8 rounded-sm px-2 py-1 hover:bg-bg [&.active]:bg-primary [&.active]:text-on-primary"
            >
              Overview
            </Link>
          </li>
        </ul>
        <div className="md:mt-auto">
          <ThemeToggle />
        </div>
      </nav>
      <main className="flex-1 p-5">
        <Outlet />
      </main>
    </div>
  )
}
