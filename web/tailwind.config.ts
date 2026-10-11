// SPDX-License-Identifier: Apache-2.0

import type { Config } from 'tailwindcss'
import plugin from 'tailwindcss/plugin'

// Colors come from the CSS variables in src/index.css (docs/design/tokens-terminal.json).
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
        raised: 'var(--color-raised)',
        amber: 'var(--color-amber)',
        cyan: 'var(--color-cyan)',
        selection: 'var(--color-selection)',
        'log-time': 'var(--color-log-time)',
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
      // The scale of tokens-terminal.json. 1rem is 13 px on a laptop and 14 px on a phone or a large screen.
      fontSize: {
        xs: ['12px', '16px'],
        sm: ['1rem', '1.5385rem'],
        base: ['1.077rem', '1.6923rem'],
        lg: ['1.23rem', '1.846rem'],
        xl: ['1.54rem', '2.154rem'],
        '2xl': ['2.154rem', '2.77rem'],
      },
      borderRadius: { none: '0', sm: '0', md: '0', lg: '2px', full: '0' },
      fontFamily: {
        sans: ['Inter Variable', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono Variable', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'monospace'],
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
