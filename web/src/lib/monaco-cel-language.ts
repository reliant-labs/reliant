/**
 * Monaco CEL Language Registration
 *
 * Registers a custom 'cel' language with Monaco Editor including:
 * - Monarch tokenizer for syntax highlighting
 * - Language configuration (brackets, auto-closing pairs)
 * - Theme token colors for both light and dark themes
 */

import type { Monaco } from '@monaco-editor/react';

// CEL namespace identifiers — these reference workflow context objects
const CEL_NAMESPACES = [
  'inputs', 'workflow', 'nodes', 'iter', 'output', 'outputs', 'trigger', 'thread',
];

/**
 * Monarch tokenizer definition for CEL (Common Expression Language).
 *
 * CEL is a simple expression language with no comments, no statements,
 * and a small set of keywords. It is used in workflow builders to
 * reference inputs, node outputs, and built-in functions.
 */
function getCELMonarchLanguage(): import('monaco-editor').languages.IMonarchLanguage {
  return {
    defaultToken: '',
    tokenPostfix: '.cel',

    namespaces: CEL_NAMESPACES,

    keywords: ['true', 'false', 'null', 'in'],

    operators: [
      '==', '!=', '<=', '>=', '&&', '||',
      '<', '>', '!', '+', '-', '*', '/', '%', '?', ':',
    ],

    symbols: /[=><!~?:&|+\-*/%]+/,

    escapes: /\\(?:[abfnrtv\\"']|x[0-9A-Fa-f]{2}|u[0-9A-Fa-f]{4}|U[0-9A-Fa-f]{8})/,

    tokenizer: {
      root: [
        // Template delimiters {{ and }}
        [/\{\{/, 'delimiter.template'],
        [/\}\}/, 'delimiter.template'],

        // Namespace identifiers (inputs, workflow, nodes, etc.)
        [/[a-zA-Z_]\w*/, {
          cases: {
            '@namespaces': 'namespace',
            '@keywords': 'keyword',
            '@default': 'identifier',
          },
        }],

        // Whitespace
        [/\s+/, 'white'],

        // Brackets and delimiters
        [/[{}()]/, '@brackets'],
        [/[[\]]/, '@brackets'],

        // Operators and symbols
        [/@symbols/, {
          cases: {
            '@operators': 'operator',
            '@default': 'delimiter',
          },
        }],

        // Dot accessor
        [/\./, 'delimiter.dot'],

        // Numbers
        [/\d+\.\d*([eE][-+]?\d+)?/, 'number.float'],
        [/\d*\.\d+([eE][-+]?\d+)?/, 'number.float'],
        [/\d+[eE][-+]?\d+/, 'number.float'],
        [/0[xX][0-9a-fA-F]+[uU]?/, 'number.hex'],
        [/\d+[uU]?/, 'number'],

        // Strings
        [/"/, 'string', '@string_double'],
        [/'/, 'string', '@string_single'],

        // Comma
        [/,/, 'delimiter.comma'],
      ],

      string_double: [
        [/[^\\"]+/, 'string'],
        [/@escapes/, 'string.escape'],
        [/\\./, 'string.escape.invalid'],
        [/"/, 'string', '@pop'],
      ],

      string_single: [
        [/[^\\']+/, 'string'],
        [/@escapes/, 'string.escape'],
        [/\\./, 'string.escape.invalid'],
        [/'/, 'string', '@pop'],
      ],
    },
  };
}

/**
 * Language configuration for CEL — brackets, auto-closing, surrounding pairs.
 */
function getCELLanguageConfiguration(): import('monaco-editor').languages.LanguageConfiguration {
  return {
    brackets: [
      ['{', '}'],
      ['[', ']'],
      ['(', ')'],
      ['{{', '}}'],
    ],
    autoClosingPairs: [
      { open: '{', close: '}' },
      { open: '[', close: ']' },
      { open: '(', close: ')' },
      { open: '"', close: '"', notIn: ['string'] },
      { open: "'", close: "'", notIn: ['string'] },
      { open: '{{', close: '}}' },
    ],
    surroundingPairs: [
      { open: '{', close: '}' },
      { open: '[', close: ']' },
      { open: '(', close: ')' },
      { open: '"', close: '"' },
      { open: "'", close: "'" },
      { open: '{{', close: '}}' },
    ],
  };
}

/**
 * Token color rules for CEL in dark themes.
 */
export const CEL_DARK_TOKEN_RULES: import('monaco-editor').editor.ITokenThemeRule[] = [
  { token: 'namespace.cel', foreground: '4EC9B0' },       // teal — workflow context objects
  { token: 'keyword.cel', foreground: '569CD6' },         // blue — true/false/null/in
  { token: 'identifier.cel', foreground: '9CDCFE' },      // light blue — general identifiers
  { token: 'string.cel', foreground: 'CE9178' },          // orange — string literals
  { token: 'string.escape.cel', foreground: 'D7BA7D' },   // gold — escape sequences
  { token: 'number.cel', foreground: 'B5CEA8' },          // green — numbers
  { token: 'number.float.cel', foreground: 'B5CEA8' },    // green — floats
  { token: 'number.hex.cel', foreground: 'B5CEA8' },      // green — hex numbers
  { token: 'operator.cel', foreground: 'D4D4D4' },        // light gray — operators
  { token: 'delimiter.cel', foreground: 'D4D4D4' },       // light gray — punctuation
  { token: 'delimiter.dot.cel', foreground: 'D4D4D4' },   // light gray — dot accessor
  { token: 'delimiter.comma.cel', foreground: 'D4D4D4' }, // light gray — comma
  { token: 'delimiter.template.cel', foreground: 'C586C0', fontStyle: 'bold' }, // purple bold — {{ }}
];

/**
 * Token color rules for CEL in light themes.
 */
export const CEL_LIGHT_TOKEN_RULES: import('monaco-editor').editor.ITokenThemeRule[] = [
  { token: 'namespace.cel', foreground: '267F99' },       // dark teal — workflow context objects
  { token: 'keyword.cel', foreground: '0000FF' },         // blue — true/false/null/in
  { token: 'identifier.cel', foreground: '001080' },      // dark blue — general identifiers
  { token: 'string.cel', foreground: 'A31515' },          // dark red — string literals
  { token: 'string.escape.cel', foreground: 'EE0000' },   // bright red — escape sequences
  { token: 'number.cel', foreground: '098658' },          // green — numbers
  { token: 'number.float.cel', foreground: '098658' },    // green — floats
  { token: 'number.hex.cel', foreground: '098658' },      // green — hex numbers
  { token: 'operator.cel', foreground: '000000' },        // black — operators
  { token: 'delimiter.cel', foreground: '000000' },       // black — punctuation
  { token: 'delimiter.dot.cel', foreground: '000000' },   // black — dot accessor
  { token: 'delimiter.comma.cel', foreground: '000000' }, // black — comma
  { token: 'delimiter.template.cel', foreground: 'AF00DB', fontStyle: 'bold' }, // purple bold — {{ }}
];

/**
 * Register the CEL language with Monaco.
 *
 * Call this after Monaco has loaded (e.g. from monacoManager or monacoTheme).
 * Safe to call multiple times — the language is only registered once.
 */
export function registerCELLanguage(monaco: Monaco): void {
  // Guard against double registration
  const languages = monaco.languages.getLanguages();
  if (languages.some(lang => lang.id === 'cel')) {
    return;
  }

  // Register the language id
  monaco.languages.register({ id: 'cel' });

  // Set the Monarch tokenizer
  monaco.languages.setMonarchTokensProvider('cel', getCELMonarchLanguage());

  // Set language configuration
  monaco.languages.setLanguageConfiguration('cel', getCELLanguageConfiguration());
}
