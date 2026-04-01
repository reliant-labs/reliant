import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { globalIgnores } from 'eslint/config'

export default tseslint.config([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs['recommended-latest'],
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    rules: {
      // Allow underscore-prefixed variables to be unused (common destructuring pattern)
      // Also allow all unused variables in general - we'll clean these up incrementally
      '@typescript-eslint/no-unused-vars': [
        'warn',
        {
          argsIgnorePattern: '^_',
          varsIgnorePattern: '^_',
          caughtErrorsIgnorePattern: '^_|^e$|^err$|^error$',
        },
      ],
      // Warn on exhaustive-deps - helps catch stale closures while not blocking builds
      // Use // eslint-disable-next-line with reason for intentional omissions
      'react-hooks/exhaustive-deps': 'warn',
      // Allow explicit any - TypeScript's any is useful for interop and dynamic code
      '@typescript-eslint/no-explicit-any': 'off',
      // Allow exports alongside components (common pattern for hooks and utilities)
      'react-refresh/only-export-components': 'off',
      // Allow lexical declarations in case blocks with proper scoping
      'no-case-declarations': 'off',
      // Downgrade empty block warning - often intentional for error handling
      'no-empty': 'warn',
      // Allow require() for dynamic imports in specific cases
      '@typescript-eslint/no-require-imports': 'warn',
      // Allow @ts-ignore where needed (prefer @ts-expect-error but don't error)
      '@typescript-eslint/ban-ts-comment': 'warn',
      // Allow empty interfaces - useful for future extension
      '@typescript-eslint/no-empty-object-type': 'off',
      // Downgrade this-alias - sometimes needed for callback context
      '@typescript-eslint/no-this-alias': 'warn',
      // Non-null assertions on optional chains - warn but don't block
      '@typescript-eslint/no-non-null-asserted-optional-chain': 'warn',
      // Useless escape - warn but don't block build
      'no-useless-escape': 'warn',
      // prefer-const - warn but don't block
      'prefer-const': 'warn',
    },
  },
])
