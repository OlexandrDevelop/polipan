// SPDX-License-Identifier: Apache-2.0

import js from '@eslint/js'
import reactHooks from 'eslint-plugin-react-hooks'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['node_modules', '../server/internal/web/dist'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  { files: ['public/**/*.js'], languageOptions: { globals: globals.browser } },
  {
    files: ['src/**/*.{ts,tsx}'],
    languageOptions: { globals: globals.browser },
    plugins: { 'react-hooks': reactHooks },
    rules: reactHooks.configs.recommended.rules,
  },
)
