import { describe, expect, it } from 'vitest'
import { parseCELContext } from '../monaco-cel-completions'

describe('parseCELContext', () => {
  describe('pure expression mode (pureExpression=true)', () => {
    const pure = true

    it('empty string at cursor 0', () => {
      expect(parseCELContext('', 0, pure)).toEqual({
        isInExpression: true,
        path: [],
        prefix: '',
        afterDot: false,
      })
    })

    it('"nodes." cursor after dot', () => {
      const text = 'nodes.'
      expect(parseCELContext(text, text.length, pure)).toEqual({
        isInExpression: true,
        path: ['nodes'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"nodes.my_llm." cursor after second dot', () => {
      const text = 'nodes.my_llm.'
      expect(parseCELContext(text, text.length, pure)).toEqual({
        isInExpression: true,
        path: ['nodes', 'my_llm'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"nodes.my_llm.sto" partial identifier after second dot', () => {
      const text = 'nodes.my_llm.sto'
      expect(parseCELContext(text, text.length, pure)).toEqual({
        isInExpression: true,
        path: ['nodes', 'my_llm'],
        prefix: 'sto',
        afterDot: true,
      })
    })

    it('"workflow." cursor after dot', () => {
      const text = 'workflow.'
      expect(parseCELContext(text, text.length, pure)).toEqual({
        isInExpression: true,
        path: ['workflow'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"workflow.bra" partial identifier', () => {
      const text = 'workflow.bra'
      expect(parseCELContext(text, text.length, pure)).toEqual({
        isInExpression: true,
        path: ['workflow'],
        prefix: 'bra',
        afterDot: true,
      })
    })

    it('"iter.iteration > 5" cursor at position 5 (after "iter.")', () => {
      // "iter." is 5 chars, cursor at 5 means right after the dot
      expect(parseCELContext('iter.iteration > 5', 5, pure)).toEqual({
        isInExpression: true,
        path: ['iter'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"inputs." cursor after dot', () => {
      const text = 'inputs.'
      expect(parseCELContext(text, text.length, pure)).toEqual({
        isInExpression: true,
        path: ['inputs'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"size(nodes." cursor after dot inside function call', () => {
      const text = 'size(nodes.'
      expect(parseCELContext(text, text.length, pure)).toEqual({
        isInExpression: true,
        path: ['nodes'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"nod" partial top-level identifier', () => {
      const text = 'nod'
      expect(parseCELContext(text, text.length, pure)).toEqual({
        isInExpression: true,
        path: [],
        prefix: 'nod',
        afterDot: false,
      })
    })

    it('"nodes.llm.tool_calls.size() > 0" cursor after .size (before parens)', () => {
      // "nodes.llm.tool_calls.size" is 25 chars
      const text = 'nodes.llm.tool_calls.size() > 0'
      const cursorAt = 'nodes.llm.tool_calls.size'.length
      expect(parseCELContext(text, cursorAt, pure)).toEqual({
        isInExpression: true,
        path: ['nodes', 'llm', 'tool_calls'],
        prefix: 'size',
        afterDot: true,
      })
    })
  })

  describe('template mode (pureExpression=false)', () => {
    const template = false

    it('"Hello {{nodes." cursor after dot inside expression', () => {
      const text = 'Hello {{nodes.'
      expect(parseCELContext(text, text.length, template)).toEqual({
        isInExpression: true,
        path: ['nodes'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"Hello {{nodes.llm.response_text}}" cursor at 18 (after "llm.")', () => {
      const text = 'Hello {{nodes.llm.response_text}}'
      // cursor at index 18 = right after the second dot (after "llm.")
      const cursor = 'Hello {{nodes.llm.'.length
      expect(parseCELContext(text, cursor, template)).toEqual({
        isInExpression: true,
        path: ['nodes', 'llm'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"Hello world" cursor at 5 → outside expression', () => {
      const result = parseCELContext('Hello world', 5, template)
      expect(result.isInExpression).toBe(false)
    })

    it('multiple expressions: cursor inside second {{ }}', () => {
      const text = '{{inputs.prompt}} and {{workflow.'
      const cursor = text.length // right after the dot
      expect(parseCELContext(text, cursor, template)).toEqual({
        isInExpression: true,
        path: ['workflow'],
        prefix: '',
        afterDot: true,
      })
    })

    it('"Text before {{" cursor right after {{', () => {
      const text = 'Text before {{'
      expect(parseCELContext(text, text.length, template)).toEqual({
        isInExpression: true,
        path: [],
        prefix: '',
        afterDot: false,
      })
    })

    it('"{{done}} more text" cursor at 15 → outside expression', () => {
      const result = parseCELContext('{{done}} more text', 15, template)
      expect(result.isInExpression).toBe(false)
    })

    it('"Value: {{inputs.mod" partial identifier inside expression', () => {
      const text = 'Value: {{inputs.mod'
      expect(parseCELContext(text, text.length, template)).toEqual({
        isInExpression: true,
        path: ['inputs'],
        prefix: 'mod',
        afterDot: true,
      })
    })
  })

  describe('edge cases', () => {
    it('cursor at 0 in empty string (pure mode)', () => {
      expect(parseCELContext('', 0, true)).toEqual({
        isInExpression: true,
        path: [],
        prefix: '',
        afterDot: false,
      })
    })

    it('cursor at 0 in empty string (template mode)', () => {
      // No {{ found → not in expression
      expect(parseCELContext('', 0, false).isInExpression).toBe(false)
    })

    it('nested braces inside expression: {{ {key: value} }}', () => {
      // Cursor inside the expression but after non-chain characters
      const text = '{{ {key: value} }}'
      // cursor at 3 (right after "{{ ") — the space breaks any chain
      expect(parseCELContext(text, 3, false)).toEqual({
        isInExpression: true,
        path: [],
        prefix: '',
        afterDot: false,
      })
    })

    it('bracket access: nodes["my-node"].field', () => {
      const text = 'nodes["my-node"].field'
      expect(parseCELContext(text, text.length, true)).toEqual({
        isInExpression: true,
        path: ['nodes', 'my-node'],
        prefix: 'field',
        afterDot: true,
      })
    })

    it('bracket access with trailing dot: nodes["my-node"].', () => {
      const text = 'nodes["my-node"].'
      expect(parseCELContext(text, text.length, true)).toEqual({
        isInExpression: true,
        path: ['nodes', 'my-node'],
        prefix: '',
        afterDot: true,
      })
    })

    it('single-quoted bracket access: nodes[\'my-node\'].field', () => {
      const text = "nodes['my-node'].field"
      expect(parseCELContext(text, text.length, true)).toEqual({
        isInExpression: true,
        path: ['nodes', 'my-node'],
        prefix: 'field',
        afterDot: true,
      })
    })

    it('cursor in middle of identifier', () => {
      // "workflow.branches" cursor at 12 = "workflow.bra"
      const text = 'workflow.branches'
      expect(parseCELContext(text, 12, true)).toEqual({
        isInExpression: true,
        path: ['workflow'],
        prefix: 'bra',
        afterDot: true,
      })
    })

    it('expression after operator: a + nodes.', () => {
      const text = 'a + nodes.'
      expect(parseCELContext(text, text.length, true)).toEqual({
        isInExpression: true,
        path: ['nodes'],
        prefix: '',
        afterDot: true,
      })
    })

    it('expression after comma in function: func(a, nodes.', () => {
      const text = 'func(a, nodes.'
      expect(parseCELContext(text, text.length, true)).toEqual({
        isInExpression: true,
        path: ['nodes'],
        prefix: '',
        afterDot: true,
      })
    })
  })
})
