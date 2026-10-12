// SPDX-License-Identifier: Apache-2.0

import { useEffect, useId, useRef, useState } from 'react'
import type {
  ButtonHTMLAttributes,
  HTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from 'react'
import { useCalm } from '@/display'
import { t, tState } from '@/i18n'
import type { Tone } from '@/status'

const touch = 'coarse:min-h-11 coarse:min-w-11'

const variants = {
  primary: 'border-primary bg-primary font-bold text-on-primary hover:brightness-110',
  secondary: 'border-border-strong bg-bg text-text hover:bg-raised',
  ghost: 'border-transparent bg-transparent text-text hover:bg-raised',
  destructive: 'border-danger bg-bg text-danger hover:bg-raised',
}

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: keyof typeof variants
  loading?: boolean
  // The action is allowed in general, but not now. The button stays focusable and says why.
  reason?: string
}

export function Button({
  variant = 'secondary',
  loading,
  reason,
  className = '',
  onClick,
  children,
  ...rest
}: ButtonProps) {
  const reasonId = useId()
  const blocked = Boolean(reason)
  return (
    <>
      <button
        type="button"
        {...rest}
        title={reason}
        aria-busy={loading || undefined}
        aria-disabled={blocked || loading || undefined}
        aria-describedby={blocked ? reasonId : undefined}
        onClick={(event) => {
          if (blocked || loading) {
            event.preventDefault()
            return
          }
          onClick?.(event)
        }}
        className={`inline-flex min-h-8 items-center justify-center gap-2 border px-3 py-1 transition-colors duration-[120ms] ${touch} ${variants[variant]} aria-pressed:border-primary aria-pressed:bg-selection aria-pressed:font-bold disabled:cursor-not-allowed disabled:opacity-60 aria-disabled:cursor-not-allowed aria-disabled:opacity-60 ${className}`}
      >
        {children}
      </button>
      {blocked && (
        <span id={reasonId} className="sr-only">
          {reason}
        </span>
      )}
    </>
  )
}

const inputClass =
  'block min-h-10 w-full border border-border-strong bg-bg px-3 py-1 text-text max-sm:text-base coarse:min-h-11 aria-[invalid=true]:border-danger'

// A small [?] that shows an explanation when you press it. Nothing is shown until you ask. The text stays in the
// page, hidden, so a screen reader can still read it for the field.
export function HintButton({
  open,
  onToggle,
  controls,
}: {
  open: boolean
  onToggle: () => void
  controls: string
}) {
  return (
    <button
      type="button"
      aria-expanded={open}
      aria-controls={controls}
      aria-label={t('common.more_info')}
      onClick={onToggle}
      className="font-term ml-2 inline-flex min-h-6 min-w-6 items-center justify-center text-muted transition-colors duration-[200ms] hover:text-primary coarse:min-h-11 coarse:min-w-11"
    >
      {open ? '[x]' : '[?]'}
    </button>
  )
}

// A paragraph of explanation that opens when you press [?].
export function Hint({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const id = useId()
  return (
    <div>
      <HintButton open={open} onToggle={() => setOpen(!open)} controls={id} />
      {open && (
        <p id={id} className="enter mt-1 max-w-prose text-muted">
          {children}
        </p>
      )}
    </div>
  )
}

interface FieldShellProps {
  id: string
  label: string
  required?: boolean
  hint?: string
  error?: string | null
  children: ReactNode
}

function FieldShell({ id, label, required, hint, error, children }: FieldShellProps) {
  const [showHint, setShowHint] = useState(false)
  return (
    <div className="space-y-1">
      <div className="flex items-center">
        <label htmlFor={id} className="font-medium">
          {label}
          {required && <span className="font-normal text-muted"> ({t('common.required')})</span>}
        </label>
        {hint && (
          <HintButton
            open={showHint}
            onToggle={() => setShowHint(!showHint)}
            controls={`${id}-hint`}
          />
        )}
      </div>
      {children}
      {hint && (
        <p id={`${id}-hint`} hidden={!showHint} className="enter text-xs text-muted">
          {hint}
        </p>
      )}
      {error && (
        <p id={`${id}-error`} className="text-sm text-danger">
          {error}
        </p>
      )}
    </div>
  )
}

function describedBy(id: string, hint?: string, error?: string | null): string | undefined {
  const ids = [hint ? `${id}-hint` : '', error ? `${id}-error` : ''].filter(Boolean)
  return ids.length > 0 ? ids.join(' ') : undefined
}

interface TextFieldProps extends InputHTMLAttributes<HTMLInputElement> {
  label: string
  hint?: string
  error?: string | null
}

export function TextField({ label, hint, error, id, className = '', ...rest }: TextFieldProps) {
  const auto = useId()
  const fieldId = id ?? auto
  return (
    <FieldShell id={fieldId} label={label} required={rest.required} hint={hint} error={error}>
      <input
        id={fieldId}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(fieldId, hint, error)}
        className={`${inputClass} ${className}`}
        {...rest}
      />
    </FieldShell>
  )
}

interface TextAreaFieldProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  label: string
  hint?: string
  error?: string | null
}

export function TextAreaField({
  label,
  hint,
  error,
  id,
  className = '',
  ...rest
}: TextAreaFieldProps) {
  const auto = useId()
  const fieldId = id ?? auto
  return (
    <FieldShell id={fieldId} label={label} required={rest.required} hint={hint} error={error}>
      <textarea
        id={fieldId}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(fieldId, hint, error)}
        className={`${inputClass} min-h-24 ${className}`}
        {...rest}
      />
    </FieldShell>
  )
}

interface SecretFieldProps extends Omit<
  InputHTMLAttributes<HTMLInputElement>,
  'type' | 'autoComplete' | 'value' | 'onChange'
> {
  label: string
  value: string
  onValue: (value: string) => void
  // The current version when a value is already stored. The value itself is never read back.
  version?: number
  error?: string | null
}

// A write-only input for a secret value (REQ-UI-005). It never shows a stored value and has no
// reveal button. The parent clears the value after save.
export function SecretField({ label, value, onValue, version, error, ...rest }: SecretFieldProps) {
  return (
    <TextField
      {...rest}
      label={label}
      type="password"
      autoComplete="new-password"
      autoCapitalize="none"
      spellCheck={false}
      data-secret-input=""
      value={value}
      onChange={(e) => onValue(e.target.value)}
      error={error}
      hint={version ? t('secret.set_version', { version }) : t('secret.write_only')}
    />
  )
}

interface SelectFieldProps extends SelectHTMLAttributes<HTMLSelectElement> {
  label: string
  hint?: string
  error?: string | null
}

export function SelectField({
  label,
  hint,
  error,
  id,
  className = '',
  children,
  ...rest
}: SelectFieldProps) {
  const auto = useId()
  const fieldId = id ?? auto
  return (
    <FieldShell id={fieldId} label={label} required={rest.required} hint={hint} error={error}>
      <select
        id={fieldId}
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy(fieldId, hint, error)}
        className={`${inputClass} ${className}`}
        {...rest}
      >
        {children}
      </select>
    </FieldShell>
  )
}

export function CheckboxField({
  label,
  hint,
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  const id = useId()
  const [showHint, setShowHint] = useState(false)
  return (
    <div>
      {/* The whole row is the label, so on touch input the target is at least 44 px (REQ-UI-017). */}
      <div className="flex items-start">
        <label className="flex min-h-8 items-start gap-3 py-1 coarse:min-h-11">
          <input
            id={id}
            type="checkbox"
            aria-describedby={hint ? `${id}-hint` : undefined}
            className="mt-0.5 size-4 shrink-0 accent-primary coarse:size-6"
            {...rest}
          />
          <span className="font-medium">{label}</span>
        </label>
        {hint && (
          <HintButton
            open={showHint}
            onToggle={() => setShowHint(!showHint)}
            controls={`${id}-hint`}
          />
        )}
      </div>
      {hint && (
        <p
          id={`${id}-hint`}
          hidden={!showHint}
          className="enter ml-7 text-xs text-muted coarse:ml-9"
        >
          {hint}
        </p>
      )}
    </div>
  )
}

const bannerStyles = {
  info: 'border-info',
  warning: 'border-warning',
  danger: 'border-danger',
}

interface BannerProps {
  variant?: keyof typeof bannerStyles
  title?: string
  children?: ReactNode
  action?: ReactNode
}

// A message in the page. It always says the next step (REQ-UI-032).
export function Banner({ variant = 'info', title, children, action }: BannerProps) {
  return (
    <div
      role={variant === 'danger' ? 'alert' : 'status'}
      className={`flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border border-l-[4px] bg-[var(--card-bg)] px-4 py-2.5 ${bannerStyles[variant]}`}
    >
      <div className="min-w-0 space-y-0.5">
        {title && <p className="font-bold">{title}</p>}
        {children && <div className="break-words text-muted">{children}</div>}
      </div>
      {action}
    </div>
  )
}

const toneClass: Record<Tone, string> = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  muted: 'text-muted',
  info: 'text-info',
}

// Each tone has its own glyph, so the state is clear without color (REQ-UI-013, T6).
const toneGlyph: Record<Tone, string> = {
  success: '●',
  warning: '▲',
  danger: '✕',
  info: '⚡',
  muted: '■',
}

// Glyph and word in the state color. No pill and no fill.
export function StatusBadge({
  tone,
  label,
  glyph,
}: {
  tone: Tone
  label: string
  // Replaces the glyph, for example a spinner on the active step.
  glyph?: ReactNode
}) {
  return (
    <span className="inline-flex items-center gap-1.5 font-bold whitespace-nowrap">
      <span aria-hidden="true" className={toneClass[tone]}>
        {glyph ?? toneGlyph[tone]}
      </span>
      <span className="text-text">{label}</span>
    </span>
  )
}

export function LockIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" fill="currentColor" aria-hidden="true">
      <rect x="1.5" y="5" width="9" height="6" rx="1" />
      <path
        d="M3.5 5V3.5a2.5 2.5 0 0 1 5 0V5"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
      />
    </svg>
  )
}

// The kind and the protected flag of an environment (design-system, section 4).
export function EnvironmentBadges({ kind, isProtected }: { kind?: string; isProtected?: boolean }) {
  const chip = 'inline-flex items-center gap-1 border px-2 py-0 text-xs font-bold'
  return (
    <>
      {kind === 'production' ? (
        <span className={`${chip} border-danger`}>{t('env.kind.production')}</span>
      ) : kind ? (
        <span className={`${chip} border-border-strong`}>{t(kindKey(kind))}</span>
      ) : null}
      {isProtected && (
        <span className={`${chip} border-danger`}>
          <LockIcon />
          {t('env.protected')}
        </span>
      )}
    </>
  )
}

function kindKey(kind: string) {
  switch (kind) {
    case 'staging':
      return 'env.kind.staging' as const
    case 'preview':
      return 'env.kind.preview' as const
    default:
      return 'env.kind.custom' as const
  }
}

export function Card({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`border border-border-strong bg-surface p-4 ${className}`}>{children}</div>
}

export function Skeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div aria-busy="true" className="space-y-3">
      <span className="sr-only">{t('common.loading')}</span>
      {Array.from({ length: lines }, (_, i) => (
        <div
          key={i}
          aria-hidden="true"
          className="h-6 animate-pulse overflow-hidden whitespace-nowrap text-border-strong"
        >
          {'░'.repeat(48 - ((i * 11) % 24))}
        </div>
      ))}
    </div>
  )
}

interface EmptyStateProps {
  title: string
  text: string
  action?: ReactNode
}

// Says what the screen is for and gives one action (REQ-UI-010).
export function EmptyState({ title, text, action }: EmptyStateProps) {
  return (
    <div className="border border-dashed border-border-strong p-6 text-center">
      <h2 className="text-lg font-bold">{title}</h2>
      <p className="prose-text mx-auto mt-1 max-w-prose text-muted">{text}</p>
      {action && <div className="mt-4 flex justify-center">{action}</div>}
    </div>
  )
}

// A bordered box with a title notch on the top line (T3). The title is an h2.
export function Panel({
  title,
  hint,
  children,
  className = '',
  ...rest
}: {
  title: string
  hint?: ReactNode
  children: ReactNode
  className?: string
} & Omit<HTMLAttributes<HTMLElement>, 'title'>) {
  // The spacing classes (space-y-*) are for the body of the card. The others (a place in a grid, a
  // border colour) are for the card itself.
  const words = className.split(/\s+/).filter(Boolean)
  const spacing = words.filter((w) => /(^|:)space-y-/.test(w)).join(' ')
  const own = words.filter((w) => !/(^|:)space-y-/.test(w)).join(' ')
  return (
    <section className={`panel ${own}`} {...rest}>
      <div className="panel-head">
        <h2 className="panel-title">{title}</h2>
        {hint && <span className="panel-hint">{hint}</span>}
      </div>
      <div className={`panel-body ${spacing}`}>{children}</div>
    </section>
  )
}

// One number of the dashboard.
export function StatCard({
  label,
  tone,
  children,
  note,
}: {
  label: string
  tone?: 'warning' | 'danger'
  children: ReactNode
  note?: ReactNode
}) {
  return (
    <div className="stat" data-tone={tone}>
      <p className="stat-label">{label}</p>
      <div className="stat-value font-term">{children}</div>
      {note && <div className="mt-2 text-muted">{note}</div>}
    </div>
  )
}

// The fields of a panel in two columns on a wide screen. A field that needs the whole row sits in
// a wrapper with the class "sm:col-span-2".
export function FieldGrid({ children }: { children: ReactNode }) {
  return <div className="grid gap-x-6 gap-y-5 sm:grid-cols-2">{children}</div>
}

// Fields that nobody has to fill in. They are hidden behind one line, and they open with a fade. A group
// that already has a value, or an error, is open from the start, so nothing is hidden that matters.
export function Optional({
  label,
  filled = false,
  children,
}: {
  label?: string
  filled?: boolean
  children: ReactNode
}) {
  const [open, setOpen] = useState(filled)
  const id = useId()
  // A value or an error that appears later opens the group.
  useEffect(() => {
    if (filled) setOpen(true)
  }, [filled])
  return (
    <div className="sm:col-span-2">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen(!open)}
        className="font-term inline-flex min-h-8 items-center gap-2 text-muted transition-colors duration-[200ms] hover:text-text coarse:min-h-11"
      >
        <span aria-hidden="true">{open ? '▾' : '▸'}</span>
        {label ?? t('common.more_options')}
      </button>
      {open && (
        <div id={id} className="enter pt-4">
          {children}
        </div>
      )}
    </div>
  )
}

export interface SectionItem {
  id: string
  label: string
  // A red item, for a danger zone.
  danger?: boolean
}

// The menu of the sections of a page. Only the chosen section is shown, so the page never opens with
// every setting at once. A vertical list on a wide screen, a row on a phone. Arrow keys move.
export function SectionNav({
  items,
  active,
  onChange,
  label,
  horizontal,
  controls = true,
}: {
  items: SectionItem[]
  active: string
  onChange: (id: string) => void
  label: string
  // Always a row, also on a wide screen (for a small choice inside a section).
  horizontal?: boolean
  // False when there are no sections with ids on the page (the tab only changes what one area shows).
  controls?: boolean
}) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({})
  function move(from: number, step: number) {
    const next = items[(from + step + items.length) % items.length]
    onChange(next.id)
    refs.current[next.id]?.focus()
  }
  return (
    <div
      role="tablist"
      aria-label={label}
      aria-orientation={horizontal ? 'horizontal' : 'vertical'}
      className={`font-term -mx-1 flex gap-1 overflow-x-auto px-1 pb-1 ${
        horizontal
          ? ''
          : 'lg:sticky lg:top-24 lg:mx-0 lg:flex-col lg:overflow-visible lg:border-l lg:border-border lg:px-0 lg:pb-0'
      }`}
    >
      {items.map((item, i) => {
        const on = item.id === active
        return (
          <button
            key={item.id}
            ref={(el) => {
              refs.current[item.id] = el
            }}
            type="button"
            role="tab"
            id={`tab-${item.id}`}
            aria-selected={on}
            aria-controls={controls ? `pane-${item.id}` : undefined}
            tabIndex={on ? 0 : -1}
            onClick={() => onChange(item.id)}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown' || e.key === 'ArrowRight') {
                e.preventDefault()
                move(i, 1)
              } else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') {
                e.preventDefault()
                move(i, -1)
              }
            }}
            className={`inline-flex min-h-9 shrink-0 items-center whitespace-nowrap border-b-2 px-3 py-1 text-left transition-colors duration-[200ms] coarse:min-h-11 ${
              horizontal ? '' : 'lg:-ml-px lg:border-b-0 lg:border-l-2 lg:whitespace-normal'
            } ${
              on
                ? `bg-selection font-bold ${item.danger ? 'border-danger text-danger' : 'border-primary text-primary'}`
                : `border-transparent hover:bg-raised ${item.danger ? 'text-danger' : 'text-muted hover:text-text'}`
            }`}
          >
            {item.label}
          </button>
        )
      })}
    </div>
  )
}

// A section of a page. Every section stays in the page, so a form keeps what you typed while you look
// at another section. Only the active one is shown, and it fades in when you open it.
export function Pane({
  id,
  active,
  children,
}: {
  id: string
  active: boolean
  children: ReactNode
}) {
  return (
    <div
      role="tabpanel"
      id={`pane-${id}`}
      aria-labelledby={`tab-${id}`}
      hidden={!active}
      className={active ? 'enter min-w-0 space-y-6' : undefined}
    >
      {children}
    </div>
  )
}

export interface Section extends SectionItem {
  content: ReactNode
}

// The menu and the sections of a simple page.
export function SectionTabs({
  sections,
  label,
  initial,
}: {
  sections: Section[]
  label: string
  initial?: string
}) {
  const [active, setActive] = useState(initial ?? sections[0]?.id ?? '')
  return (
    <div className="grid items-start gap-6 lg:grid-cols-[13rem_minmax(0,1fr)] lg:gap-8">
      <SectionNav items={sections} active={active} onChange={setActive} label={label} />
      <div className="min-w-0">
        {sections.map((s) => (
          <Pane key={s.id} id={s.id} active={s.id === active}>
            {s.content}
          </Pane>
        ))}
      </div>
    </div>
  )
}

// The phases of a deployment, in order. A state that has no segment of its own (image ready, shifting, draining)
// shows on the nearest one.
const PHASES = ['queued', 'building', 'starting', 'health_checking', 'switching', 'live'] as const
const PHASE_OF: Record<string, number> = {
  queued: 0,
  building: 1,
  image_ready: 2,
  starting: 2,
  health_checking: 3,
  shifting: 4,
  switching: 4,
  draining: 4,
  live: 5,
}

// The progress of a deployment as segments. The phase that runs has a moving light. A finished deployment is full,
// and a failed one shows the phase where it stopped in red.
export function DeployProgress({
  state,
  failedAfter,
  compact,
}: {
  state: string | undefined
  // The last good state of a failed deployment.
  failedAfter?: string
  compact?: boolean
}) {
  const failed = state === 'failed'
  // The phase that runs now, or for a failed deployment the first phase that did not finish.
  const at = failed ? (PHASE_OF[failedAfter ?? ''] ?? -1) + 1 : (PHASE_OF[state ?? ''] ?? -1)
  const done = state === 'live'
  const label = tState('status.deployment', state)
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={PHASES.length - 1}
      aria-valuenow={Math.max(0, Math.min(at, PHASES.length - 1))}
      aria-valuetext={label}
      className="space-y-2"
    >
      <div className="flex gap-1">
        {PHASES.map((p, i) => (
          <span
            key={p}
            className="seg"
            data-state={
              done || i < at ? 'done' : i === at ? (failed ? 'failed' : 'active') : 'idle'
            }
          />
        ))}
      </div>
      {!compact && (
        <ol className="flex justify-between gap-1 text-xs text-muted">
          {PHASES.map((p, i) => (
            <li key={p} className={i === at && !done ? 'font-bold text-text' : undefined}>
              {tState('status.deployment', p)}
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}

// A bar with a light that moves, for work that has no known steps.
export function BusyBar() {
  return <div aria-hidden="true" className="sweep" />
}

const METER_CELLS = 12

// A text meter like htop: [|||||     ] 41%. The value is also text in the label (T7).
export function Meter({ value, label }: { value: number; label: string }) {
  const pct = Math.max(0, Math.min(100, Math.round(value)))
  const filled = Math.round((pct / 100) * METER_CELLS)
  const tone = pct >= 90 ? 'meter-bad' : pct >= 70 ? 'meter-warn' : 'meter-ok'
  return (
    <span role="img" aria-label={`${label} ${pct}%`} className="meter">
      <span aria-hidden="true">
        [<span className={tone}>{'|'.repeat(filled)}</span>
        {' '.repeat(METER_CELLS - filled)}] {String(pct).padStart(3, ' ')}%
      </span>
    </span>
  )
}

// A key chip. It is hidden from screen readers: the button carries aria-keyshortcuts.
export function KeyHint({ k, children }: { k: string; children?: ReactNode }) {
  return (
    <span className="hidden whitespace-nowrap text-muted sm:inline">
      <kbd aria-hidden="true" className="key">
        {k}
      </kbd>
      {children && <span aria-hidden="true"> {children}</span>}
    </span>
  )
}

const SPINNER_FRAMES = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']

// A braille spinner for the active step: a text swap every 90 ms, so nothing moves. In Calm mode
// it is the static glyph of the state.
export function Spinner({ fallback = '▲' }: { fallback?: string }) {
  const { calm } = useCalm()
  const [frame, setFrame] = useState(0)
  useEffect(() => {
    if (calm) return
    const id = window.setInterval(() => setFrame((f) => (f + 1) % SPINNER_FRAMES.length), 90)
    return () => window.clearInterval(id)
  }, [calm])
  return <>{calm ? fallback : SPINNER_FRAMES[frame]}</>
}
