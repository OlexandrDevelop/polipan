// SPDX-License-Identifier: Apache-2.0

import { t } from '@/i18n'
import type { Node } from '@/scope'

function bytes(n: number): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GiB`
  if (n >= 1 << 20) return `${Math.round(n / (1 << 20))} MiB`
  return `${n} B`
}

function Bar({
  label,
  used,
  total,
  text,
}: {
  label: string
  used?: number
  total?: number
  text: string
}) {
  const known = used !== undefined && total !== undefined && total > 0
  const percent = known ? Math.min(100, Math.round((used / total) * 100)) : 0
  const tone = percent >= 90 ? 'bg-danger' : percent >= 75 ? 'bg-warning' : 'bg-primary'
  return (
    <div className="min-w-32">
      <div className="flex justify-between gap-2 text-xs">
        <span>{label}</span>
        <span className="text-muted">{known ? `${percent}%` : '-'}</span>
      </div>
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        aria-valuetext={known ? text : t('nodes.usage_unknown')}
        className="h-2 w-full overflow-hidden rounded-sm bg-surface"
      >
        <div className={`h-full ${tone}`} style={{ width: `${percent}%` }} />
      </div>
      <div className="text-xs text-muted">{known ? text : t('nodes.usage_unknown')}</div>
    </div>
  )
}

// CPU, memory and disk of a node with the numbers next to the bars (REQ-UI-020: the number is always shown).
export function NodeBars({ node }: { node: Node }) {
  const cap = node.capacity ?? {}
  const use = node.usage ?? {}
  const cpuLoad = use.cpu_millis !== undefined ? use.cpu_millis : undefined
  return (
    <div className="grid gap-2 sm:grid-cols-3">
      <Bar
        label={t('nodes.cpu')}
        used={cpuLoad}
        total={cap.cpu_millis}
        text={`${((cpuLoad ?? 0) / 1000).toFixed(1)} / ${((cap.cpu_millis ?? 0) / 1000).toFixed(0)} ${t('nodes.cores')}`}
      />
      <Bar
        label={t('nodes.memory')}
        used={use.memory_bytes}
        total={cap.memory_bytes}
        text={`${bytes(use.memory_bytes ?? 0)} / ${bytes(cap.memory_bytes ?? 0)}`}
      />
      <Bar
        label={t('nodes.disk')}
        used={use.disk_bytes}
        total={cap.disk_bytes}
        text={`${bytes(use.disk_bytes ?? 0)} / ${bytes(cap.disk_bytes ?? 0)}`}
      />
    </div>
  )
}
