import type { PrismTheme } from 'prism-react-renderer';

/**
 * Reliant Dark Theme - matches Monaco's VSCode Dark (vs-dark) theme exactly
 * Colors from VSCode's Dark+ theme used by Monaco
 */
export const reliantDarkTheme: PrismTheme = {
  plain: {
    color: '#D4D4D4',
    backgroundColor: '#1E1E1E',
  },
  styles: [
    {
      types: ['comment', 'prolog', 'doctype', 'cdata'],
      style: {
        color: '#6A9955',
        fontStyle: 'italic',
      },
    },
    {
      types: ['namespace'],
      style: {
        color: '#4EC9B0',
      },
    },
    {
      types: ['string', 'attr-value'],
      style: {
        color: '#CE9178',
      },
    },
    {
      types: ['punctuation', 'operator'],
      style: {
        color: '#D4D4D4',
      },
    },
    {
      types: ['entity', 'url'],
      style: {
        color: '#569CD6',
      },
    },
    {
      types: ['number', 'boolean', 'constant'],
      style: {
        color: '#B5CEA8',
      },
    },
    {
      types: ['variable'],
      style: {
        color: '#9CDCFE',
      },
    },
    {
      types: ['property', 'property-access'],
      style: {
        color: '#9CDCFE',
      },
    },
    {
      types: ['symbol', 'regex', 'inserted'],
      style: {
        color: '#D16969',
      },
    },
    {
      types: ['atrule', 'keyword', 'attr-name'],
      style: {
        color: '#C586C0',
      },
    },
    {
      types: ['selector'],
      style: {
        color: '#D7BA7D',
      },
    },
    {
      types: ['function'],
      style: {
        color: '#DCDCAA',
      },
    },
    {
      types: ['function-variable'],
      style: {
        color: '#DCDCAA',
      },
    },
    {
      types: ['deleted', 'tag'],
      style: {
        color: '#569CD6',
      },
    },
    {
      types: ['class-name'],
      style: {
        color: '#4EC9B0',
      },
    },
    {
      types: ['builtin', 'char'],
      style: {
        color: '#4EC9B0',
      },
    },
    {
      types: ['important', 'bold'],
      style: {
        fontWeight: 'bold',
      },
    },
    {
      types: ['italic'],
      style: {
        fontStyle: 'italic',
      },
    },
  ],
};

/**
 * Reliant Light Theme - matches Monaco's VSCode Light (vs) theme exactly
 * Colors from VSCode's Light+ theme used by Monaco
 */
export const reliantLightTheme: PrismTheme = {
  plain: {
    color: '#000000',
    backgroundColor: '#FFFFFF',
  },
  styles: [
    {
      types: ['comment', 'prolog', 'doctype', 'cdata'],
      style: {
        color: '#008000',
        fontStyle: 'italic',
      },
    },
    {
      types: ['namespace'],
      style: {
        color: '#267F99',
      },
    },
    {
      types: ['string', 'attr-value'],
      style: {
        color: '#A31515',
      },
    },
    {
      types: ['punctuation', 'operator'],
      style: {
        color: '#000000',
      },
    },
    {
      types: ['entity', 'url'],
      style: {
        color: '#0000FF',
      },
    },
    {
      types: ['number', 'boolean', 'constant'],
      style: {
        color: '#098658',
      },
    },
    {
      types: ['variable'],
      style: {
        color: '#001080',
      },
    },
    {
      types: ['property', 'property-access'],
      style: {
        color: '#001080',
      },
    },
    {
      types: ['symbol', 'regex', 'inserted'],
      style: {
        color: '#811F3F',
      },
    },
    {
      types: ['atrule', 'keyword', 'attr-name'],
      style: {
        color: '#AF00DB',
      },
    },
    {
      types: ['selector'],
      style: {
        color: '#800000',
      },
    },
    {
      types: ['function'],
      style: {
        color: '#795E26',
      },
    },
    {
      types: ['function-variable'],
      style: {
        color: '#795E26',
      },
    },
    {
      types: ['deleted', 'tag'],
      style: {
        color: '#0000FF',
      },
    },
    {
      types: ['class-name'],
      style: {
        color: '#267F99',
      },
    },
    {
      types: ['builtin', 'char'],
      style: {
        color: '#267F99',
      },
    },
    {
      types: ['important', 'bold'],
      style: {
        fontWeight: 'bold',
      },
    },
    {
      types: ['italic'],
      style: {
        fontStyle: 'italic',
      },
    },
  ],
};
