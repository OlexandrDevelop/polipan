// SPDX-License-Identifier: Apache-2.0

import { Link, useRouterState } from '@tanstack/react-router'
import { useState } from 'react'
import type { ReactNode } from 'react'
import { useAccess } from '@/permissions'
import { t } from '@/i18n'
import { CreateProjectDialog } from '@/routes/Projects'
import { AddNodeDialog } from '@/routes/Nodes'
import { useEnvironments, useNodes, useProjects, useServices } from '@/scope'
import type { Environment, Project } from '@/scope'
import { serviceTone } from '@/status'
import type { Tone } from '@/status'

export interface SidebarLink {
  to: string
  label: string
  exact?: boolean
  // A page of the settings group.
  settings?: boolean
}

const dot: Record<Tone, string> = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  info: 'text-info',
  muted: 'text-muted',
}

const row =
  'relative flex min-h-9 w-full items-center gap-2 pr-4 text-left text-text transition-colors duration-[200ms] hover:bg-selection/60 [&.active]:bg-selection [&.active]:font-bold [&.active]:text-primary [&.active]:before:absolute [&.active]:before:inset-y-0 [&.active]:before:left-0 [&.active]:before:w-[3px] [&.active]:before:bg-primary'

// The indent of a row of the tree.
const pad = (depth: number) => ({ paddingLeft: `${1.25 + depth * 1}rem` })

function Chevron({ open }: { open: boolean }) {
  return (
    <span aria-hidden="true" className="w-3 shrink-0 text-xs text-muted">
      {open ? '▾' : '▸'}
    </span>
  )
}

// A line that opens and closes a branch.
function Branch({
  depth,
  label,
  index,
  open,
  onToggle,
  children,
}: {
  depth: number
  label: ReactNode
  index?: string
  open: boolean
  onToggle: () => void
  children: ReactNode
}) {
  return (
    <li>
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className={row}
        style={pad(depth)}
      >
        {index !== undefined && (
          <span aria-hidden="true" className="w-5 text-xs text-muted">
            {index}
          </span>
        )}
        <span className="min-w-0 flex-1 truncate">{label}</span>
        <Chevron open={open} />
      </button>
      {open && <ul className="enter">{children}</ul>}
    </li>
  )
}

// A branch whose name is a link to its page. The arrow opens and closes it, and opening the page opens it too.
function LinkBranch({
  depth,
  open,
  onToggle,
  onOpen,
  link,
  children,
}: {
  depth: number
  open: boolean
  onToggle: () => void
  onOpen: () => void
  link: (props: { className: string; onClick: () => void }) => ReactNode
  children: ReactNode
}) {
  return (
    <li>
      <div className="flex items-stretch" style={pad(depth)}>
        <button
          type="button"
          aria-expanded={open}
          aria-label={t('common.expand')}
          onClick={onToggle}
          className="flex w-5 shrink-0 items-center justify-start text-muted hover:text-text"
        >
          <Chevron open={open} />
        </button>
        {link({ className: `${row} !pl-0 flex-1`, onClick: onOpen })}
      </div>
      {open && <ul className="enter">{children}</ul>}
    </li>
  )
}

function AddRow({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <li>
      <button
        type="button"
        onClick={onClick}
        className={`${row} text-muted hover:text-text`}
        style={pad(1.5)}
      >
        <span aria-hidden="true">+</span>
        {label}
      </button>
    </li>
  )
}

function ServiceLeaf({
  org,
  project,
  env,
  service,
}: {
  org: string
  project: string
  env: string
  service: { name?: string; status?: string }
}) {
  return (
    <li>
      <Link
        to="/o/$org/p/$project/e/$env/s/$service"
        params={{ org, project, env, service: service.name ?? '' }}
        className={row}
        style={pad(3)}
      >
        <span aria-hidden="true" className={`text-xs ${dot[serviceTone(service.status)]}`}>
          ●
        </span>
        <span className="truncate">{service.name}</span>
      </Link>
    </li>
  )
}

function EnvBranch({
  org,
  project,
  env,
  path,
}: {
  org: string
  project: string
  env: Environment
  path: string
}) {
  const slug = env.slug ?? ''
  const here = path.includes(`/p/${project}/e/${slug}`)
  const [open, setOpen] = useState(here)
  const services = useServices(open ? env.id : undefined)
  return (
    <LinkBranch
      depth={1.5}
      open={open}
      onToggle={() => setOpen(!open)}
      onOpen={() => setOpen(true)}
      link={(p) => (
        <Link
          to="/o/$org/p/$project/e/$env"
          params={{ org, project, env: slug }}
          activeOptions={{ exact: true }}
          className={p.className}
          onClick={p.onClick}
        >
          <span className="truncate">{env.name}</span>
        </Link>
      )}
    >
      {(services.data ?? []).map((s) => (
        <ServiceLeaf key={s.id} org={org} project={project} env={slug} service={s} />
      ))}
    </LinkBranch>
  )
}

function ProjectBranch({ org, project, path }: { org: string; project: Project; path: string }) {
  const slug = project.slug ?? ''
  const here = path.includes(`/p/${slug}`)
  const [open, setOpen] = useState(here)
  const envs = useEnvironments(open ? project.id : undefined)
  return (
    <LinkBranch
      depth={0.5}
      open={open}
      onToggle={() => setOpen(!open)}
      onOpen={() => setOpen(true)}
      link={(p) => (
        <Link
          to="/o/$org/p/$project"
          params={{ org, project: slug }}
          activeOptions={{ exact: true }}
          className={p.className}
          onClick={p.onClick}
        >
          <span className="truncate">{project.name}</span>
        </Link>
      )}
    >
      {(envs.data ?? [])
        .filter((e) => e.kind !== 'preview')
        .map((e) => (
          <EnvBranch key={e.id} org={org} project={slug} env={e} path={path} />
        ))}
    </LinkBranch>
  )
}

function ProjectsTree({
  org,
  index,
  label,
  path,
}: {
  org: string
  index: string
  label: string
  path: string
}) {
  const projects = useProjects()
  const canCreate = useAccess('project:create') === 'allow'
  const [open, setOpen] = useState(path.includes('/p/') || path.endsWith('/projects'))
  const [adding, setAdding] = useState(false)
  return (
    <>
      <Branch depth={0} index={index} label={label} open={open} onToggle={() => setOpen(!open)}>
        {(projects.data ?? []).map((p) => (
          <ProjectBranch key={p.id} org={org} project={p} path={path} />
        ))}
        {canCreate && <AddRow label={t('projects.create')} onClick={() => setAdding(true)} />}
      </Branch>
      {adding && <CreateProjectDialog onClose={() => setAdding(false)} />}
    </>
  )
}

function NodesTree({
  org,
  index,
  label,
  path,
}: {
  org: string
  index: string
  label: string
  path: string
}) {
  const nodes = useNodes()
  const canManage = useAccess('node:manage') === 'allow'
  const [open, setOpen] = useState(path.includes('/nodes'))
  const [adding, setAdding] = useState(false)
  return (
    <>
      <Branch depth={0} index={index} label={label} open={open} onToggle={() => setOpen(!open)}>
        {(nodes.data ?? []).map((n) => (
          <li key={n.id}>
            <Link
              to="/o/$org/nodes/$node"
              params={{ org, node: n.id ?? '' }}
              className={row}
              style={pad(1.5)}
            >
              <span
                aria-hidden="true"
                className={`text-xs ${n.state === 'ready' ? 'text-success' : 'text-warning'}`}
              >
                ●
              </span>
              <span className="truncate">{n.name}</span>
            </Link>
          </li>
        ))}
        {canManage && <AddRow label={t('nodes.add')} onClick={() => setAdding(true)} />}
      </Branch>
      {adding && <AddNodeDialog onClose={() => setAdding(false)} />}
    </>
  )
}

// The menu on the left of a wide screen. It is a tree: a branch opens to show what is in it (the projects
// and what is in each project, the servers), so there is no page that only lists them. The settings pages
// are one more branch, and the user is at the bottom.
export function Sidebar({
  org,
  links,
  name,
  role,
  onSignOut,
  signingOut,
}: {
  org: string
  links: SidebarLink[]
  name: string
  role: string
  onSignOut: () => void
  signingOut: boolean
}) {
  const path = useRouterState({ select: (s) => s.location.pathname })
  const work = links.filter((l) => !l.settings)
  const settings = links.filter((l) => l.settings)
  const inSettings = path.includes('/settings/')
  const [open, setOpen] = useState(inSettings)
  const expanded = open || inSettings
  const initials =
    name
      .split(/[\s@._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0]?.toUpperCase())
      .join('') || '?'
  const index = (i: number) => String(i + 1).padStart(2, '0')
  return (
    <aside
      aria-label={t('nav.main')}
      className="font-term sticky top-14 hidden h-[calc(100vh-3.5rem-2.75rem)] flex-col overflow-y-auto border-r border-[var(--card-line)] bg-[var(--card-bg)] lg:col-start-1 lg:row-start-2 lg:flex"
    >
      <nav className="flex-1 py-5">
        <p className="px-5 pb-3 text-xs uppercase tracking-[0.16em] text-muted">
          {t('nav.workspace')}
        </p>
        <ul>
          {work.map((l, i) =>
            l.to === '/o/$org/projects' ? (
              <ProjectsTree key={l.to} org={org} index={index(i)} label={l.label} path={path} />
            ) : l.to === '/o/$org/nodes' ? (
              <NodesTree key={l.to} org={org} index={index(i)} label={l.label} path={path} />
            ) : (
              <li key={l.to}>
                <Link
                  to={l.to}
                  params={{ org }}
                  activeOptions={l.exact ? { exact: true } : undefined}
                  className={row}
                  style={pad(0)}
                >
                  <span aria-hidden="true" className="w-5 text-xs text-muted">
                    {index(i)}
                  </span>
                  {l.label}
                </Link>
              </li>
            ),
          )}
        </ul>
        {settings.length > 0 && (
          <ul className="mt-5 border-t border-[var(--card-line)] pt-3">
            <Branch
              depth={0}
              index={index(work.length)}
              label={t('nav.settings')}
              open={expanded}
              onToggle={() => setOpen(!open)}
            >
              {settings.map((l) => (
                <li key={l.to}>
                  <Link to={l.to} params={{ org }} className={row} style={pad(1.5)}>
                    {l.label}
                  </Link>
                </li>
              ))}
            </Branch>
          </ul>
        )}
      </nav>
      <div className="flex items-center gap-3 border-t border-[var(--card-line)] px-5 py-4">
        <span
          aria-hidden="true"
          className="grid size-9 shrink-0 place-items-center border border-primary text-xs font-bold text-primary"
        >
          {initials}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-semibold">{name}</span>
          <span className="block truncate text-xs text-muted">{role}</span>
        </span>
        <button
          type="button"
          aria-busy={signingOut || undefined}
          onClick={onSignOut}
          className="text-xs text-muted transition-colors hover:text-text"
        >
          {t('nav.sign_out')}
        </button>
      </div>
    </aside>
  )
}
