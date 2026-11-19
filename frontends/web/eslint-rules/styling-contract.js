/**
 * The web-styling contract (.reliant/skills/web-styling/SKILL.md) as lint
 * rules. WARN-ONLY by design: they name the drift the restyle phases remove,
 * call site by call site, and must not fail a build while the legacy sites
 * are still there.
 *
 *   tiny-text          text-2xs / text-3xs outside badges and chart axes
 *                      (src/styles/stylingContract.json `tinyText.allow`):
 *                      text-meta is the floor for content
 *   uppercase-tracking `uppercase` with `tracking-*`: labels are sentence
 *                      case; the one exception is the violet eyebrow
 *   raw-palette        Tailwind palette colors (`bg-blue-500`) and hex
 *                      classes in chrome, outside data visualization
 *                      (`dataViz`): use the semantic tokens
 *   muted-structure    `bg-muted/NN` with no state variant: bg-muted is
 *                      hover/selected/disabled state, never structure (an
 *                      inset is bg-background + border)
 *
 * Each rule reads every string literal and template chunk, so a class built
 * in a helper or a cn() argument is seen too.
 */
import fs from 'node:fs'

const contract = JSON.parse(fs.readFileSync(new URL('../src/styles/stylingContract.json', import.meta.url), 'utf-8'))

/** A tiny glob: `**` spans directories, `*` stays within one. */
export function globToRegExp(glob) {
  let body = ''
  for (let i = 0; i < glob.length; i += 1) {
    if (glob.startsWith('**/', i)) {
      body += '(?:.*/)?'
      i += 2
    } else if (glob.startsWith('**', i)) {
      body += '.*'
      i += 1
    } else if (glob[i] === '*') {
      body += '[^/]*'
    } else {
      body += glob[i].replace(/[.+?^${}()|[\]\\]/g, '\\$&')
    }
  }
  return new RegExp(`(^|/)${body}$`)
}

const TINY_ALLOW = contract.tinyText.allow.map(globToRegExp)
const DATA_VIZ = contract.dataViz.map(globToRegExp)
const SKIP = [/\/components\/forge-ui\//, /\/src\/gen\//, /\/__tests__\//, /\.test\.[jt]sx?$/, /\.spec\.[jt]sx?$/]

const VARIANTS = '(?:[a-z0-9-[\\]=&_:.@/]+:)*'
const TINY = new RegExp(`(?:^|\\s)${VARIANTS}(text-(?:2xs|3xs))(?=\\s|$)`, 'g')
const PALETTE =
  'red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone'
const RAW = new RegExp(
  `(?:^|\\s)${VARIANTS}((?:bg|text|border|ring|from|via|to|fill|stroke|outline|decoration|divide|shadow|caret|placeholder)-(?:(?:${PALETTE})-(?:50|[1-9]00|950)|\\[#[0-9a-fA-F]{3,8}\\])(?:\\/\\d+)?)(?=\\s|$)`,
  'g',
)
// No variant prefix: hover:bg-muted/40 and data-[state=open]:bg-muted/50 are state.
const MUTED = /(?:^|\s)(bg-muted\/\d+)(?=\s|$)/g

function stringsOf(context, check) {
  const filename = context.filename ?? context.getFilename()
  if (SKIP.some((re) => re.test(filename))) return {}
  return {
    Literal(node) {
      if (typeof node.value === 'string') check(node, node.value, filename)
    },
    TemplateElement(node) {
      check(node, node.value.raw, filename)
    },
  }
}

function rule(description, messages, check) {
  return {
    meta: { type: 'suggestion', docs: { description }, messages, schema: [] },
    create(context) {
      return stringsOf(context, (node, value, filename) => check(context, node, value, filename))
    },
  }
}

export default {
  rules: {
    'tiny-text': rule(
      'text-2xs/text-3xs only in badges and chart axes',
      { tiny: '`{{cls}}` is for badges and chart axes only. Content uses a type role: `text-meta` is the floor (web-styling, "Type roles").' },
      (context, node, value, filename) => {
        if (TINY_ALLOW.some((re) => re.test(filename))) return
        for (const m of value.matchAll(TINY)) context.report({ node, messageId: 'tiny', data: { cls: m[1] } })
      },
    ),
    'uppercase-tracking': rule(
      'labels are sentence case',
      { upper: 'Labels are sentence case, `font-medium`, muted: no `uppercase tracking-*` (web-styling, "Type roles"). The violet eyebrow is the one exception.' },
      (context, node, value) => {
        const classes = value.split(/\s+/)
        if (!classes.includes('uppercase') || !classes.some((c) => /^tracking-/.test(c))) return
        if (classes.includes('text-brand-violet')) return
        context.report({ node, messageId: 'upper' })
      },
    ),
    'raw-palette': rule(
      'semantic color tokens in chrome',
      { raw: '`{{cls}}` is a raw palette color. Chrome uses semantic tokens (`text-primary-ink`, `bg-card`, `text-success-ink`, …); raw hues are for data visualization only (web-styling, "Brand roles").' },
      (context, node, value, filename) => {
        if (DATA_VIZ.some((re) => re.test(filename))) return
        for (const m of value.matchAll(RAW)) context.report({ node, messageId: 'raw', data: { cls: m[1] } })
      },
    ),
    'muted-structure': rule(
      'bg-muted is interaction state only',
      { muted: '`{{cls}}` builds structure from `bg-muted`, which is hover/selected/disabled state only and inverts between light and dark. An inset is `bg-background` + `border` (web-styling, "Elevation").' },
      (context, node, value) => {
        for (const m of value.matchAll(MUTED)) context.report({ node, messageId: 'muted', data: { cls: m[1] } })
      },
    ),
  },
}
