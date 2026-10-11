// SPDX-License-Identifier: Apache-2.0

import { lazy } from 'react'
import type { ComponentType, LazyExoticComponent } from 'react'

// A component from a module with named exports, loaded when it is first shown. Heavy parts
// (charts, log viewer, terminal) stay out of the first bundle (quality, section 3).
export function lazyNamed<K extends string, P>(
  load: () => Promise<Record<K, ComponentType<P>>>,
  name: K,
): LazyExoticComponent<ComponentType<P>> {
  return lazy(async () => ({ default: (await load())[name] }))
}
