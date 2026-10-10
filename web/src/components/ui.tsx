// SPDX-License-Identifier: Apache-2.0

import { useId } from 'react'
import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
} from 'react'
import { t } from '@/i18n'
import type { Tone } from '@/status'

const touch = 'coarse:min-h-11 coarse:min-w-11'

const variants = {
  primary: 'border-primary bg-primary font-semibold text-on-primary',
  secondary: 'border-border-strong bg-bg text-text hover:bg-surface',
  ghost: 'border-transparent bg-transparent text-text hover:bg-surface',
  destructive: 'border-danger bg-bg text-danger hover:bg-surface',
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
        className={`inline-flex min-h-8 items-center justify-center gap-2 rounded-sm border px-3 py-1 text-sm ${touch} ${variants[variant]} disabled:cursor-not-allowed disabled:opacity-60 aria-disabled:cursor-not-allowed aria-disabled:opacity-60 ${className}`}
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
  'block min-h-10 w-full rounded-sm border border-border-strong bg-bg px-3 py-1 text-base text-text coarse:min-h-11 aria-[invalid=true]:border-danger'

interface FieldShellProps {
  id: string
  label: string
  required?: boolean
  hint?: string
  error?: string | null
  children: ReactNode
}

function FieldShell({ id, label, required, hint, error, children }: FieldShellProps) {
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-sm font-medium">
        {label}
        {required && <span className="font-normal text-muted"> ({t('common.required')})</span>}
      </label>
      {children}
      {hint && (
        <p id={`${id}-hint`} className="text-sm text-muted">
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
  return (
    <div className="flex items-start gap-3">
      <input
        id={id}
        type="checkbox"
        aria-describedby={hint ? `${id}-hint` : undefined}
        className="mt-1 size-4 accent-primary coarse:size-6"
        {...rest}
      />
      <div>
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        {hint && (
          <p id={`${id}-hint`} className="text-sm text-muted">
            {hint}
          </p>
        )}
      </div>
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
      className={`flex flex-wrap items-center justify-between gap-3 rounded-sm border border-l-[6px] bg-surface p-3 ${bannerStyles[variant]}`}
    >
      <div className="min-w-0 space-y-1 text-sm">
        {title && <p className="font-semibold">{title}</p>}
        {children && <div className="break-words">{children}</div>}
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

// Each tone has its own shape, so the state is clear without color (REQ-UI-013).
function ToneIcon({ tone }: { tone: Tone }) {
  const common = { width: 12, height: 12, viewBox: '0 0 12 12', 'aria-hidden': true } as const
  switch (tone) {
    case 'success':
      return (
        <svg {...common} fill="currentColor">
          <circle cx="6" cy="6" r="5" />
        </svg>
      )
    case 'warning':
      return (
        <svg {...common} fill="currentColor">
          <path d="M6 1 11.5 11H0.5z" />
        </svg>
      )
    case 'danger':
      return (
        <svg {...common} fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M2 2 10 10M10 2 2 10" />
        </svg>
      )
    case 'info':
      return (
        <svg {...common} fill="currentColor">
          <path d="M7 0 2 7h3l-1 5 6-7H7z" />
        </svg>
      )
    default:
      return (
        <svg {...common} fill="currentColor">
          <rect x="1" y="1" width="10" height="10" />
        </svg>
      )
  }
}

export function StatusBadge({ tone, label }: { tone: Tone; label: string }) {
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full border border-current px-2.5 py-0.5 text-xs font-semibold ${toneClass[tone]}`}
    >
      <ToneIcon tone={tone} />
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
  const chip =
    'inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs font-semibold'
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
  return (
    <div className={`rounded-md border border-border bg-surface p-4 ${className}`}>{children}</div>
  )
}

export function Skeleton({ lines = 3 }: { lines?: number }) {
  return (
    <div aria-busy="true" className="space-y-3">
      <span className="sr-only">{t('common.loading')}</span>
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} aria-hidden="true" className="h-6 animate-pulse rounded-sm bg-surface" />
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
    <div className="rounded-md border border-dashed border-border-strong p-6 text-center">
      <h2 className="text-lg font-semibold">{title}</h2>
      <p className="mx-auto mt-1 max-w-prose text-muted">{text}</p>
      {action && <div className="mt-4 flex justify-center">{action}</div>}
    </div>
  )
}
