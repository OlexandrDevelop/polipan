// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useSyncExternalStore } from 'react'

// Calm mode (REQ-UI-039) and optional effects (REQ-UI-040). Both are saved on the device and set
// as attributes of <html> (data-calm, data-fx). /theme-init.js sets them before the first paint.
const CALM_KEY = 'polipan.calm'
const FX_KEY = 'polipan.fx'
const CHANGE = 'polipan-display'

export const FX_STEPS = ['off', 'scanlines', 'glow', 'curve'] as const
export type Fx = (typeof FX_STEPS)[number]

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function subscribe(notify: () => void) {
  window.addEventListener(CHANGE, notify)
  const media = window.matchMedia('(prefers-reduced-motion: reduce)')
  media.addEventListener('change', notify)
  return () => {
    window.removeEventListener(CHANGE, notify)
    media.removeEventListener('change', notify)
  }
}

export function osReducesMotion(): boolean {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

// True when motion is off: the switch is on, or the OS asks for less motion.
export function calmNow(): boolean {
  return read(CALM_KEY) === 'on' || osReducesMotion()
}

function fxNow(): Fx {
  const v = read(FX_KEY)
  return FX_STEPS.includes(v as Fx) ? (v as Fx) : 'off'
}

export function apply() {
  const root = document.documentElement
  root.setAttribute('data-calm', calmNow() ? 'on' : 'off')
  root.setAttribute('data-fx', fxNow())
}

export function useCalm(): { calm: boolean; byOs: boolean; toggle: () => void } {
  const calm = useSyncExternalStore(subscribe, calmNow, () => false)
  const byOs = useSyncExternalStore(subscribe, osReducesMotion, () => false)
  const toggle = useCallback(() => {
    // The OS setting wins. The switch cannot turn motion back on against it (v1).
    if (osReducesMotion()) return
    try {
      localStorage.setItem(CALM_KEY, calmNow() ? 'off' : 'on')
    } catch {
      // Storage can be blocked. The choice then lasts only for this page.
      document.documentElement.setAttribute('data-calm', calmNow() ? 'off' : 'on')
      return
    }
    window.dispatchEvent(new Event(CHANGE))
  }, [])
  useEffect(apply, [calm])
  return { calm, byOs, toggle }
}

export function useFx(): { fx: Fx; cycle: () => void } {
  const fx = useSyncExternalStore(subscribe, fxNow, () => 'off' as Fx)
  const cycle = useCallback(() => {
    const next = FX_STEPS[(FX_STEPS.indexOf(fxNow()) + 1) % FX_STEPS.length]
    try {
      localStorage.setItem(FX_KEY, next)
    } catch {
      // Storage can be blocked.
    }
    window.dispatchEvent(new Event(CHANGE))
  }, [])
  useEffect(apply, [fx])
  return { fx, cycle }
}
