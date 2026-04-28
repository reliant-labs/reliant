const tailwindAtRules = [
  'config',
  'custom-variant',
  'layer',
  'reference',
  'source',
  'tailwind',
  'theme',
  'utility',
  'variant',
]

export default {
  extends: ['stylelint-config-standard'],
  rules: {
    'at-rule-no-unknown': [
      true,
      {
        ignoreAtRules: tailwindAtRules,
      },
    ],
    'function-no-unknown': [
      true,
      {
        ignoreFunctions: ['theme'],
      },
    ],

    // Keep CSS lint focused on correctness and maintainability instead of churny formatting.
    'alpha-value-notation': null,
    'at-rule-empty-line-before': null,
    'color-function-alias-notation': null,
    'color-function-notation': null,
    'color-hex-length': null,
    'comment-empty-line-before': null,
    'declaration-block-single-line-max-declarations': null,
    'declaration-empty-line-before': null,
    'font-family-name-quotes': null,
    'hue-degree-notation': null,
    'import-notation': null,
    'keyframes-name-pattern': null,
    'media-feature-range-notation': null,
    'no-descending-specificity': null,
    'no-invalid-position-at-import-rule': null,
    'property-no-vendor-prefix': null,
    'rule-empty-line-before': null,
    'selector-class-pattern': null,
    'selector-not-notation': null,
    'value-keyword-case': null,
  },
}
