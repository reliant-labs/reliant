/**
 * Monaco CEL Completion Provider
 *
 * Implements CompletionItemProvider for CEL expressions in the workflow builder.
 * Provides context-aware autocompletions for namespaces, node outputs, functions,
 * and workflow inputs based on cursor position and expression context.
 */

import type { Monaco } from '@monaco-editor/react'
import {
  getCELNamespaces,
  getCELGlobalFunctions,
  getCELMemberFunctions,
  getNodeOutputSchema,
  getNamespaceFields,
} from './cel-completion-service'
import type { CELFieldInfo } from '../gen/reliant/v1/catalog_pb'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type IDisposable = { dispose(): void }

export interface CELCompletionContext {
  /** Available node IDs in the workflow (e.g., ["call_llm", "run_tests"]) */
  nodeIds: string[]
  /** Maps nodeId → nodeType (e.g., { "call_llm": "call_llm", "run_tests": "run" }) */
  nodeTypeMap: Record<string, string>
  /** Workflow input parameters */
  inputParams: Record<string, { type: string; description?: string }>
  /** CEL context determines which namespaces are available */
  celContext: 'default' | 'loop_while' | 'edge_condition' | 'save_message' | 'thread'
  /** Whether entire value is CEL (true) or template mode with {{ }} (false) */
  pureExpression: boolean
  /** Current node type — used to resolve `output.*` fields in save_message/loop contexts */
  currentNodeType?: string
  /** Per-node declared output keys (e.g., router declared outputs) */
  nodeDeclaredOutputs?: Record<string, string[]>
}

export interface ParsedCELContext {
  /** Whether the cursor is inside a CEL expression */
  isInExpression: boolean
  /** Dot-separated path segments before cursor, e.g. ["nodes", "my_llm"] */
  path: string[]
  /** Partial text being typed after the last dot, e.g. "sto" in "nodes.llm.sto" */
  prefix: string
  /** Whether cursor is right after a dot */
  afterDot: boolean
}

// ---------------------------------------------------------------------------
// Namespace filtering by CEL context
// ---------------------------------------------------------------------------

const CONTEXT_NAMESPACES: Record<CELCompletionContext['celContext'], string[]> = {
  default: ['inputs', 'workflow', 'nodes', 'iter'],
  loop_while: ['outputs', 'iter', 'inputs'],
  edge_condition: ['inputs', 'workflow', 'nodes', 'iter', 'outputs'],
  save_message: ['inputs', 'workflow', 'nodes', 'output'],
  thread: ['workflow', 'nodes'],
}

// ---------------------------------------------------------------------------
// Context Parser (exported for testing)
// ---------------------------------------------------------------------------

/**
 * Parse the CEL expression context at the given cursor position.
 *
 * Determines whether the cursor is inside a CEL expression and extracts
 * the dot-path leading up to the cursor for completion resolution.
 */
export function parseCELContext(
  text: string,
  cursorOffset: number,
  pureExpression: boolean,
): ParsedCELContext {
  const notInExpression: ParsedCELContext = {
    isInExpression: false,
    path: [],
    prefix: '',
    afterDot: false,
  }

  // Determine expression start offset
  let exprStart: number
  if (pureExpression) {
    exprStart = 0
  } else {
    // Template mode: find the innermost {{ before cursor without a matching }}
    exprStart = -1
    let i = 0
    while (i < cursorOffset) {
      if (i + 1 < cursorOffset && text[i] === '{' && text[i + 1] === '{') {
        exprStart = i + 2 // position right after {{
        i += 2
        continue
      }
      if (i + 1 <= cursorOffset && text[i] === '}' && text[i + 1] === '}') {
        exprStart = -1 // closed the expression
        i += 2
        continue
      }
      i++
    }
    if (exprStart === -1) {
      return notInExpression
    }
  }

  // Extract the expression text from exprStart to cursorOffset
  const exprText = text.slice(exprStart, cursorOffset)

  // Walk backwards from the end to find the chain of identifiers and dots.
  // We stop at characters that break a member-access chain:
  //   operators, parens (open), commas, whitespace, brackets (but handle ["..."] indexing).
  const chain = extractChain(exprText)

  // Split chain on "." to get path segments + prefix
  if (chain === '') {
    return {
      isInExpression: true,
      path: [],
      prefix: '',
      afterDot: false,
    }
  }

  const afterDot = chain.endsWith('.')
  // Split into segments, filter out empty strings from trailing dots
  const parts = chain.split('.').filter((s) => s !== '')

  if (afterDot) {
    // All parts are path segments, prefix is empty
    return {
      isInExpression: true,
      path: parts,
      prefix: '',
      afterDot: true,
    }
  }

  // The last part is the prefix being typed
  const prefix = parts.length > 0 ? parts[parts.length - 1] : ''
  const path = parts.slice(0, -1)

  return {
    isInExpression: true,
    path,
    prefix,
    afterDot: path.length > 0, // there was a dot before prefix if path has segments
  }
}

/**
 * Extract the member-access chain ending at the cursor position.
 *
 * Walks backwards through the expression text to find a continuous chain of
 * identifiers, dots, and bracket-access patterns (e.g., `nodes["my-id"].field`).
 * Stops at operators, open parens, commas, and other chain-breaking characters.
 */
function extractChain(exprText: string): string {
  let i = exprText.length - 1
  const parts: string[] = []

  while (i >= 0) {
    const ch = exprText[i]

    // Skip whitespace at current position — but whitespace breaks the chain
    // (e.g., "foo .bar" — the space means "foo" is not part of bar's chain)
    if (/\s/.test(ch)) {
      break
    }

    // Dot: part of the chain
    if (ch === '.') {
      parts.push('.')
      i--
      continue
    }

    // Identifier character: collect the full identifier going backwards
    if (/[a-zA-Z0-9_]/.test(ch)) {
      let ident = ''
      while (i >= 0 && /[a-zA-Z0-9_]/.test(exprText[i])) {
        ident = exprText[i] + ident
        i--
      }
      parts.push(ident)
      continue
    }

    // Closing bracket ] — might be bracket-access like ["my-id"]
    if (ch === ']') {
      const bracketResult = consumeBracketAccess(exprText, i)
      if (bracketResult) {
        // Replace with the extracted key as a path segment.
        // Insert a dot separator so the key becomes its own segment
        // when the chain is split on '.'
        parts.push(bracketResult.key)
        parts.push('.')
        i = bracketResult.newIndex
        continue
      }
      // Not a recognizable bracket access — break chain
      break
    }

    // Closing paren ) — skip balanced parens (e.g., `size(x)`)
    // The paren group result is part of the chain but we treat it
    // as an opaque expression. We skip it and continue to see if
    // there's more chain before it (e.g., `func(x).field`).
    if (ch === ')') {
      const parenEnd = skipBalancedParens(exprText, i)
      if (parenEnd >= 0) {
        i = parenEnd - 1
        // Now we should be at the function name or a dot before the paren.
        // We don't add the function call to the chain path since
        // we can't know the return type. Instead, break here and let
        // the resolver handle it with member functions.
        // But first, check if there's an identifier (function name) to skip
        if (i >= 0 && /[a-zA-Z0-9_]/.test(exprText[i])) {
          // Skip the function name
          while (i >= 0 && /[a-zA-Z0-9_]/.test(exprText[i])) {
            i--
          }
          // If preceded by a dot, continue the chain
          if (i >= 0 && exprText[i] === '.') {
            // We discard everything we collected so far after the paren
            // because we can't resolve through function calls.
            // Actually, let's just break — the expression before the function
            // call is a different context.
            break
          }
        }
        break
      }
      break
    }

    // Any other character breaks the chain (operators, open parens, commas, etc.)
    break
  }

  // Reverse and join since we walked backwards
  parts.reverse()
  return parts.join('')
}

/**
 * Try to consume a bracket-access like `["my-id"]` ending at position `endIdx`
 * (which points to the `]`).
 *
 * Returns the extracted key and the new index (pointing to just before the `[`),
 * or null if it's not a recognizable bracket-access pattern.
 */
function consumeBracketAccess(
  text: string,
  endIdx: number,
): { key: string; newIndex: number } | null {
  // endIdx points to ']'
  // We expect: ["..."] or ['...']
  let j = endIdx - 1

  // Skip whitespace before ]
  while (j >= 0 && /\s/.test(text[j])) j--

  // Expect closing quote
  if (j < 0) return null
  const quote = text[j]
  if (quote !== '"' && quote !== "'") return null
  j--

  // Collect the key string (backwards)
  let key = ''
  while (j >= 0 && text[j] !== quote) {
    key = text[j] + key
    j--
  }
  if (j < 0 || text[j] !== quote) return null
  j-- // skip opening quote

  // Skip whitespace after [
  while (j >= 0 && /\s/.test(text[j])) j--

  // Expect [
  if (j < 0 || text[j] !== '[') return null

  return { key, newIndex: j - 1 }
}

/**
 * Skip balanced parentheses backwards from position `endIdx` (which points to `)``).
 * Returns the index of the matching `(`, or -1 if not found.
 */
function skipBalancedParens(text: string, endIdx: number): number {
  let depth = 0
  for (let j = endIdx; j >= 0; j--) {
    if (text[j] === ')') depth++
    else if (text[j] === '(') {
      depth--
      if (depth === 0) return j
    }
  }
  return -1
}

// ---------------------------------------------------------------------------
// Completion Resolver
// ---------------------------------------------------------------------------

interface CompletionEntry {
  label: string
  kind: 'namespace' | 'field' | 'function' | 'method' | 'variable'
  insertText: string
  detail: string
  documentation: string
  sortGroup: number // 0 = namespaces, 1 = fields/variables, 2 = functions
}

/**
 * Resolve completions based on parsed context and dynamic workflow context.
 */
function resolveCompletions(
  parsed: ParsedCELContext,
  ctx: CELCompletionContext,
): CompletionEntry[] {
  if (!parsed.isInExpression) return []

  const allowedNamespaces = CONTEXT_NAMESPACES[ctx.celContext] ?? CONTEXT_NAMESPACES.default

  // No path — top-level completions
  if (parsed.path.length === 0 && !parsed.afterDot) {
    return [
      ...getNamespaceCompletions(allowedNamespaces),
      ...getGlobalFunctionCompletions(),
    ]
  }

  const root = parsed.path[0]

  // path = ["nodes"]
  if (root === 'nodes' && parsed.path.length === 1) {
    return getNodeIdCompletions(ctx)
  }

  // path = ["nodes", "<id>"]
  if (root === 'nodes' && parsed.path.length === 2) {
    const nodeId = parsed.path[1]
    const nodeType = ctx.nodeTypeMap[nodeId]
    if (nodeType) {
      const schemaFields = getFieldCompletions(getNodeOutputSchema(nodeType))
      // Merge declared output keys (e.g., from router outputs map)
      const declaredKeys = ctx.nodeDeclaredOutputs?.[nodeId] ?? []
      const declaredFields: CompletionEntry[] = declaredKeys
        .filter((k) => !schemaFields.some((f) => f.label === k))
        .map((k) => ({
          label: k,
          kind: 'field',
          insertText: k,
          detail: 'declared output',
          documentation: `Declared output of node "${nodeId}"`,
          sortGroup: 1,
        }))
      return [
        ...declaredFields,
        ...schemaFields,
        ...getMemberFunctionCompletions(),
      ]
    }
    return getMemberFunctionCompletions()
  }

  // path = ["nodes", "<id>", ...deeper]
  if (root === 'nodes' && parsed.path.length > 2) {
    return getMemberFunctionCompletions()
  }

  // path = ["inputs"]
  if (root === 'inputs' && parsed.path.length === 1) {
    return getInputParamCompletions(ctx)
  }

  // path = ["output"] or ["outputs"]
  if ((root === 'output' || root === 'outputs') && parsed.path.length === 1) {
    // Try node-type-specific output schema first (from currentNodeType),
    // then fall back to static namespace fields
    const nodeTypeFields = ctx.currentNodeType
      ? getNodeOutputSchema(ctx.currentNodeType)
      : []
    const staticFields = getNamespaceFields(root)
    const fields = nodeTypeFields.length > 0 ? nodeTypeFields : staticFields
    return [
      ...getFieldCompletions(fields),
      ...getMemberFunctionCompletions(),
    ]
  }

  // path = [known_namespace] for static namespaces (workflow, iter, etc.)
  if (parsed.path.length === 1) {
    const fields = getNamespaceFields(root)
    if (fields.length > 0) {
      return [
        ...getFieldCompletions(fields),
        ...getMemberFunctionCompletions(),
      ]
    }
  }

  // Deeper paths or unknown — offer member functions
  return getMemberFunctionCompletions()
}

function getNamespaceCompletions(allowed: string[]): CompletionEntry[] {
  const allNamespaces = getCELNamespaces()
  const entries: CompletionEntry[] = []

  for (const ns of allNamespaces) {
    if (!allowed.includes(ns.name)) continue
    entries.push({
      label: ns.name,
      kind: 'namespace',
      insertText: ns.name + '.',
      detail: 'namespace',
      documentation: ns.description,
      sortGroup: 0,
    })
  }

  // Also add allowed namespaces that might not be in the static list
  // (e.g., "inputs" is dynamic but should still appear)
  for (const name of allowed) {
    if (!entries.some((e) => e.label === name)) {
      entries.push({
        label: name,
        kind: 'namespace',
        insertText: name + '.',
        detail: 'namespace',
        documentation: '',
        sortGroup: 0,
      })
    }
  }

  return entries
}

function getGlobalFunctionCompletions(): CompletionEntry[] {
  return getCELGlobalFunctions().map((fn) => ({
    label: fn.name,
    kind: 'function' as const,
    insertText: fn.name + '(',
    detail: fn.signature,
    documentation: fn.description + (fn.example ? `\n\nExample: ${fn.example}` : ''),
    sortGroup: 2,
  }))
}

function getMemberFunctionCompletions(): CompletionEntry[] {
  return getCELMemberFunctions().map((fn) => ({
    label: fn.name,
    kind: 'method' as const,
    insertText: fn.name + '(',
    detail: fn.signature,
    documentation: fn.description + (fn.example ? `\n\nExample: ${fn.example}` : ''),
    sortGroup: 2,
  }))
}

function getFieldCompletions(fields: CELFieldInfo[]): CompletionEntry[] {
  return fields.map((f) => ({
    label: f.name,
    kind: 'field' as const,
    insertText: f.name,
    detail: f.type,
    documentation: f.description,
    sortGroup: 1,
  }))
}

function getNodeIdCompletions(ctx: CELCompletionContext): CompletionEntry[] {
  return ctx.nodeIds.map((id) => ({
    label: id,
    kind: 'variable' as const,
    insertText: id + '.',
    detail: ctx.nodeTypeMap[id] ?? 'node',
    documentation: `Output of node "${id}"`,
    sortGroup: 1,
  }))
}

function getInputParamCompletions(ctx: CELCompletionContext): CompletionEntry[] {
  return Object.entries(ctx.inputParams).map(([name, info]) => ({
    label: name,
    kind: 'variable' as const,
    insertText: name,
    detail: info.type,
    documentation: info.description ?? '',
    sortGroup: 1,
  }))
}

// ---------------------------------------------------------------------------
// CompletionItem Mapping (Monaco-specific)
// ---------------------------------------------------------------------------

function toMonacoKind(
  monaco: Monaco,
  kind: CompletionEntry['kind'],
): number {
  switch (kind) {
    case 'namespace':
      return monaco.languages.CompletionItemKind.Module
    case 'field':
      return monaco.languages.CompletionItemKind.Field
    case 'function':
      return monaco.languages.CompletionItemKind.Function
    case 'method':
      return monaco.languages.CompletionItemKind.Method
    case 'variable':
      return monaco.languages.CompletionItemKind.Variable
  }
}

// ---------------------------------------------------------------------------
// Singleton Provider with Context Registry
// ---------------------------------------------------------------------------

// Map of editor model URI → context getter
const contextRegistry = new Map<string, () => CELCompletionContext>()
let providerRegistered = false

/**
 * Register a CEL completion context for a specific editor model.
 *
 * A single global completion provider is registered once (singleton) and
 * dispatches to the context for whichever model is requesting completions.
 *
 * Returns an IDisposable that unregisters this editor's context.
 */
export function registerCELEditorContext(
  monaco: Monaco,
  modelUri: string,
  getContext: () => CELCompletionContext,
): IDisposable {
  if (!providerRegistered) {
    monaco.languages.registerCompletionItemProvider('cel', {
      triggerCharacters: ['.', '{'],

      provideCompletionItems(model: import('monaco-editor').editor.ITextModel, position: import('monaco-editor').Position) {
        const uri = model.uri.toString()
        const getCtx = contextRegistry.get(uri)
        if (!getCtx) return { suggestions: [] }

        const ctx = getCtx()
        const textUntilPosition = model.getValueInRange({
          startLineNumber: 1,
          startColumn: 1,
          endLineNumber: position.lineNumber,
          endColumn: position.column,
        })

        // Handle `{` trigger — only provide completions when forming `{{`
        const charBeforeCursor = position.column > 1
          ? model.getValueInRange({
              startLineNumber: position.lineNumber,
              startColumn: position.column - 1,
              endLineNumber: position.lineNumber,
              endColumn: position.column,
            })
          : ''

        // If the trigger was `{`, check if it forms `{{`
        if (charBeforeCursor === '{') {
          const charBeforeThat = position.column > 2
            ? model.getValueInRange({
                startLineNumber: position.lineNumber,
                startColumn: position.column - 2,
                endLineNumber: position.lineNumber,
                endColumn: position.column - 1,
              })
            : ''
          if (charBeforeThat !== '{') {
            // Single `{` — not a template opening, no completions
            return { suggestions: [] }
          }
          // It's `{{` — provide top-level completions for template mode
          if (!ctx.pureExpression) {
            const allowedNamespaces = CONTEXT_NAMESPACES[ctx.celContext] ?? CONTEXT_NAMESPACES.default
            const entries = [
              ...getNamespaceCompletions(allowedNamespaces),
              ...getGlobalFunctionCompletions(),
            ]
            const word = model.getWordUntilPosition(position)
            const range = {
              startLineNumber: position.lineNumber,
              startColumn: word.startColumn,
              endLineNumber: position.lineNumber,
              endColumn: position.column,
            }
            return {
              suggestions: entries.map((entry) => ({
                label: entry.label,
                kind: toMonacoKind(monaco, entry.kind),
                insertText: entry.insertText,
                detail: entry.detail,
                documentation: entry.documentation,
                range,
                sortText: entry.sortGroup + entry.label,
              })),
            }
          }
        }

        // Parse context at cursor
        const cursorOffset = textUntilPosition.length
        const parsed = parseCELContext(textUntilPosition, cursorOffset, ctx.pureExpression)

        if (!parsed.isInExpression) {
          return { suggestions: [] }
        }

        // Resolve completions
        const entries = resolveCompletions(parsed, ctx)

        // Filter by prefix if one is being typed
        const filtered = parsed.prefix
          ? entries.filter((e) =>
              e.label.toLowerCase().startsWith(parsed.prefix.toLowerCase()),
            )
          : entries

        // Build Monaco range — replace from word start to cursor
        const word = model.getWordUntilPosition(position)
        const range = {
          startLineNumber: position.lineNumber,
          startColumn: word.startColumn,
          endLineNumber: position.lineNumber,
          endColumn: position.column,
        }

        return {
          suggestions: filtered.map((entry) => ({
            label: entry.label,
            kind: toMonacoKind(monaco, entry.kind),
            insertText: entry.insertText,
            detail: entry.detail,
            documentation: entry.documentation,
            range,
            sortText: entry.sortGroup + entry.label,
          })),
        }
      },
    })
    providerRegistered = true
  }

  contextRegistry.set(modelUri, getContext)

  return {
    dispose() {
      contextRegistry.delete(modelUri)
    },
  }
}