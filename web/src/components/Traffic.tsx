// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react'
import { calmNow, useCalm } from '@/display'
import { t } from '@/i18n'

const PACKETS = 6

// True while the element is on screen and the tab is visible. Motion stops when it is not
// (terminal-style, section 5, rule 6).
export function useRunning<T extends Element>() {
  const ref = useRef<T>(null)
  const [onScreen, setOnScreen] = useState(true)
  const [visible, setVisible] = useState(() => document.visibilityState !== 'hidden')
  useEffect(() => {
    const el = ref.current
    if (!el || typeof IntersectionObserver === 'undefined') return
    const watch = new IntersectionObserver((entries) =>
      setOnScreen(entries[0]?.isIntersecting ?? true),
    )
    watch.observe(el)
    return () => watch.disconnect()
  }, [])
  useEffect(() => {
    const onChange = () => setVisible(document.visibilityState !== 'hidden')
    document.addEventListener('visibilitychange', onChange)
    return () => document.removeEventListener('visibilitychange', onChange)
  }, [])
  return { ref, running: onScreen && visible }
}

interface LaneProps {
  label: string
  percent: number
  // 'old' packets are info colored, 'new' packets are primary colored.
  kind: 'old' | 'new'
  // How many of the packets run in this lane.
  packets: number
  moving: boolean
}

function Lane({ label, percent, kind, packets, moving }: LaneProps) {
  const text = t('deployment.traffic_value', { label, percent })
  const color = kind === 'new' ? 'bg-primary' : 'bg-info'
  return (
    <div className="space-y-1">
      <p>{text}</p>
      <div className="flex items-center gap-2">
        <div
          role="progressbar"
          aria-label={text}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          aria-valuetext={text}
          className="packet-track h-3 min-w-0 flex-1 rounded-full bg-surface"
        >
          {/* The share is a scaled fill: transform only, no width. */}
          <div
            className="fill-bar absolute inset-0 bg-selection"
            style={{ transform: `scaleX(${percent / 100})` }}
          />
          {moving &&
            Array.from({ length: packets }, (_, i) => (
              <span
                key={i}
                aria-hidden="true"
                className={`packet ${color}`}
                style={{ animationDelay: `${-(i * 1600) / Math.max(packets, 1)}ms` }}
              />
            ))}
        </div>
        <span className="w-[5ch] text-right">{percent}%</span>
      </div>
    </div>
  )
}

// UI-08, section 5.2: two lanes, old version and new version. Packets move along each lane and
// their count follows the split (6 in total). A paused or finished rollout and Calm mode show no
// packets: the percent, the fill and the text are the whole state.
export function TrafficLanes({
  oldPercent,
  newPercent,
  oldLabel,
  newLabel,
  flowing,
}: {
  oldPercent: number
  newPercent: number
  oldLabel: string
  newLabel: string
  flowing: boolean
}) {
  const { calm } = useCalm()
  const { ref, running } = useRunning<HTMLDivElement>()
  const moving = flowing && running && !calm && !calmNow()
  const total = oldPercent + newPercent
  const newPackets = total > 0 ? Math.round((PACKETS * newPercent) / total) : 0
  return (
    <div ref={ref} className="space-y-2">
      <Lane
        label={oldLabel}
        percent={oldPercent}
        kind="old"
        packets={PACKETS - newPackets}
        moving={moving}
      />
      <Lane label={newLabel} percent={newPercent} kind="new" packets={newPackets} moving={moving} />
    </div>
  )
}
