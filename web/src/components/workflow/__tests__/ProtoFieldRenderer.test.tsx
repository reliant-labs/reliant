import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import type { ProtoFieldSchema } from '../../../types/workflowFieldSchema'
import { ProtoFieldRenderer } from '../ProtoFieldRenderer'

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
    // CEL text/textarea fields now show a toggle instead of a static badge.
    // When value contains {{ }}, it auto-detects into CEL mode and shows the Braces toggle.
    expect(screen.getByTitle('Use CEL expression')).toBeInTheDocument()
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
