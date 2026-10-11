// SPDX-License-Identifier: Apache-2.0

import type { Config } from 'tailwindcss'
import plugin from 'tailwindcss/plugin'

// Colors come from the CSS variables in src/index.css (docs/spec/ui/design-tokens.json).
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  darkMode: ['selector', '[data-theme="dark"]'],
  theme: {
    screens: {
      sm: '640px',
      lg: '1024px',
      '2xl': '1536px',
      coarse: { raw: '(pointer: coarse)' },
    },
    extend: {
      colors: {
        bg: 'var(--color-bg)',
        surface: 'var(--color-surface)',
        text: 'var(--color-text)',
        muted: 'var(--color-muted)',
        border: 'var(--color-border)',
        'border-strong': 'var(--color-border-strong)',
        primary: 'var(--color-primary)',
        'on-primary': 'var(--color-on-primary)',
        success: 'var(--color-success)',
        warning: 'var(--color-warning)',
        danger: 'var(--color-danger)',
        info: 'var(--color-info)',
      },
      borderRadius: { sm: '6px', md: '10px', lg: '14px' },
      fontFamily: {
        sans: ['Inter Variable', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono Variable', 'ui-monospace', 'monospace'],
      },
    },
  },
  plugins: [
    // The custom "coarse" screen stops Tailwind from making max-* variants, so "max-sm" is
    // added here. It is the phone layout (below 640 px).
    plugin(({ addVariant }) => {
      addVariant('max-sm', '@media (max-width: 639.98px)')
    }),
  ],
} satisfies Config
