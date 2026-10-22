import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import type { ProtoFieldSchema } from '../../../types/workflowFieldSchema'
import { ProtoFieldRenderer } from '../ProtoFieldRenderer'

// The CEL editor fetches its completion catalog when it mounts. These tests
// render the editor but never complete anything, and unmocked the fetch was a
// real RPC that settled after the test.
vi.mock('../../../lib/cel-completion-service', async () => ({
  ...(await vi.importActual<typeof import('../../../lib/cel-completion-service')>(
    '../../../lib/cel-completion-service',
  )),
  ensureCELCompletionsCached: async () => {},
}))

function createSchema(overrides: Partial<ProtoFieldSchema> = {}): ProtoFieldSchema {
  return {
    key: 'test-field',
    label: 'Test Field',
    widget: 'text',
    ...overrides,
  }
}

describe('ProtoFieldRenderer', () => {
  it('hides fields when required context keys are missing', () => {
    const onChange = vi.fn()

    const { container, rerender } = render(
      <ProtoFieldRenderer
        schema={createSchema({ requiresContext: ['chatId'] })}
        value=""
        onChange={onChange}
        context={{}}
      />
    )

    expect(container.firstChild).toBeNull()

    rerender(
      <ProtoFieldRenderer
        schema={createSchema({ requiresContext: ['chatId'] })}
        value=""
        onChange={onChange}
        context={{ chatId: 'chat-1' }}
      />
    )

    expect(screen.getByLabelText('Test Field')).toBeInTheDocument()
  })

  it('renders select and emits selected string value', () => {
    const onChange = vi.fn()

    const { rerender } = render(
      <ProtoFieldRenderer
        schema={createSchema({
          key: 'role',
          label: 'Role',
          widget: 'select',
          options: [
            { value: 'user', label: 'User' },
            { value: 'assistant', label: 'Assistant' },
          ],
        })}
        value=""
        onChange={onChange}
      />
    )

    const select = screen.getByLabelText('Role') as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'assistant' } })

    expect(onChange).toHaveBeenCalledWith('assistant')

    rerender(
      <ProtoFieldRenderer
        schema={createSchema({
          key: 'role',
          label: 'Role',
          widget: 'select',
          options: [
            { value: 'user', label: 'User' },
            { value: 'assistant', label: 'Assistant' },
          ],
        })}
        value="assistant"
        onChange={onChange}
      />
    )

    expect((screen.getByLabelText('Role') as HTMLSelectElement).value).toBe('assistant')
  })

  // Select chrome is owned by the .cpv2-field-select rule in config-panel.css,
  // which is where the --config-input-* tokens and the ring focus treatment now
  // live. jsdom does not load that stylesheet, so the ownership class at the
  // component boundary is what this can assert.
  it('applies the config select ownership class for select chrome', () => {
    const onChange = vi.fn()

    render(
      <ProtoFieldRenderer
        schema={createSchema({
          key: 'role',
          label: 'Role',
          widget: 'select',
          options: [
            { value: 'user', label: 'User' },
            { value: 'assistant', label: 'Assistant' },
          ],
        })}
        value="user"
        onChange={onChange}
      />
    )

    const select = screen.getByLabelText('Role')
    expect(select.className).toContain('cpv2-field-select')
  })

  it('renders checkbox and emits boolean values', () => {
    const onChange = vi.fn()

    render(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'enabled', label: 'Enabled', widget: 'checkbox' })}
        value={false}
        onChange={onChange}
      />
    )

    const toggle = screen.getByRole('switch', { name: 'Enabled' })
    expect(toggle).toHaveAttribute('aria-checked', 'false')

    fireEvent.click(toggle)

    expect(onChange).toHaveBeenCalledWith(true)
  })

  it('renders text input and emits typed value', () => {
    const onChange = vi.fn()

    render(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'title', label: 'Title', widget: 'text' })}
        value=""
        onChange={onChange}
      />
    )

    const input = screen.getByLabelText('Title')
    fireEvent.change(input, { target: { value: 'new value' } })

    expect(onChange).toHaveBeenCalledWith('new value')
  })

  it('normalizes CEL-capable string wrappers for display', () => {
    const onChange = vi.fn()

    render(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'prompt', label: 'Prompt', widget: 'text', celCapable: true })}
        value={{ value: { case: 'expr', value: '{{input.prompt}}' } }}
        onChange={onChange}
      />
    )

    const input = screen.getByDisplayValue('{{input.prompt}}') as HTMLInputElement
    expect(input.value).toBe('{{input.prompt}}')
    // CEL text/textarea fields show a Fixed / Expression toggle instead of a
    // static badge. A value containing {{ }} auto-detects into Expression mode.
    expect(screen.getByRole('button', { name: 'Expression' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Fixed' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('normalizes CEL-capable boolean wrappers for checkbox state', () => {
    const onChange = vi.fn()

    render(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'memo', label: 'Memo', widget: 'checkbox', celCapable: true })}
        value={{ value: { case: 'literal', value: true } }}
        onChange={onChange}
      />
    )

    const toggle = screen.getByRole('switch', { name: 'Memo' })
    expect(toggle).toHaveAttribute('aria-checked', 'true')
  })

  it('renders CEL input instead of checkbox when value is a boolean CEL expression', () => {
    const onChange = vi.fn()

    render(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'debug', label: 'Debug', widget: 'checkbox', celCapable: true })}
        value={{ value: { case: 'expr', value: '{{inputs.debug_mode}}' } }}
        onChange={onChange}
      />
    )

    // Should render a CEL text input, not a toggle
    expect(screen.queryByRole('switch')).toBeNull()
    const input = screen.getByDisplayValue('{{inputs.debug_mode}}') as HTMLInputElement
    expect(input.value).toBe('{{inputs.debug_mode}}')
  })
})

// "Lots of inputs are super unclear about what values look like": an empty
// box with only a mode toggle. Every field now says what goes in it.
describe('ProtoFieldRenderer says what a value looks like', () => {
  it('prints the description under the input, describes the input with it, and shows the example', () => {
    render(
      <ProtoFieldRenderer
        schema={createSchema({
          key: 'sid',
          label: 'Message SID *',
          description: 'The SM… id returned by Send message.',
          example: 'SM0123456789abcdef0123456789abcdef',
        })}
        value=""
        onChange={vi.fn()}
      />,
    )

    const input = screen.getByLabelText('Message SID *')
    expect(input).toHaveAttribute('placeholder', 'SM0123456789abcdef0123456789abcdef')
    const hint = screen.getByText('The SM… id returned by Send message.')
    expect(input).toHaveAttribute('aria-describedby', hint.id)
    // The description is not repeated behind a ? when there is nothing more to say.
    expect(screen.queryByRole('button', { name: 'Help' })).toBeNull()
  })

  it('keeps the ? for what the description does not say, and shows the type beside the label', () => {
    render(
      <ProtoFieldRenderer
        schema={createSchema({
          key: 'tool_calls',
          label: 'Tool calls',
          description: 'The tool calls a Call LLM step returned.',
          helpText: 'Default: none',
          typeHint: 'list of tool calls',
        })}
        value=""
        onChange={vi.fn()}
      />,
    )
    expect(screen.getByText('list of tool calls')).toBeInTheDocument()
    expect(screen.getByText('The tool calls a Call LLM step returned.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Help' })).toBeInTheDocument()
  })

  it('shows the expression example in Expression mode, and the shape of one when the example is a literal', () => {
    const { rerender } = render(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'tool_calls', label: 'Tool calls', celCapable: true, example: '{{nodes.call_llm.tool_calls}}' })}
        value=""
        onChange={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Expression' }))
    expect(screen.getByRole('button', { name: 'Expression' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByPlaceholderText('{{nodes.call_llm.tool_calls}}')).toBeInTheDocument()

    rerender(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'to', label: 'To', celCapable: true, example: '+15551234567' })}
        value=""
        onChange={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Expression' }))
    expect(screen.getByPlaceholderText('{{nodes.<step>.<output>}}')).toBeInTheDocument()
  })

  it('says what an unset field defaults to instead of showing an empty box', () => {
    render(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'timeout', label: 'Timeout (seconds)', widget: 'number', defaultValue: 30, example: '10' })}
        value={undefined}
        onChange={vi.fn()}
      />,
    )
    expect(screen.getByLabelText('Timeout (seconds)')).toHaveAttribute('placeholder', 'Default: 30')
  })

  it('labels the value-mode toggle in words, with pressed state', () => {
    render(
      <ProtoFieldRenderer
        schema={createSchema({ key: 'to', label: 'To', celCapable: true })}
        value=""
        onChange={vi.fn()}
      />,
    )
    const group = screen.getByRole('group', { name: 'To: value mode' })
    expect(group).toHaveTextContent('Fixed')
    expect(group).toHaveTextContent('Expression')
    expect(screen.getByRole('button', { name: 'Fixed' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Expression' })).toHaveAttribute('aria-pressed', 'false')
  })
})
