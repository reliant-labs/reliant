/**
 * List-valued config fields (proto `CelStringList`, e.g. call_llm's `skills`)
 * are stored as `{ values: [...] }` but edited as comma-separated text. Both
 * halves of that conversion have to hold, or the field silently refuses input:
 * the read side has to render the stored list back as text, and the edit has
 * to survive the separator characters that have no place in the stored array.
 */
import { describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { mergeStepUpdate, type Step } from '../../../types/workflow'
import { ActionStepConfig } from '../config/ActionStepConfig'
import { getActionArgValue } from '../../../lib/actionStepArgs'
import type { NodeInfo, NodeInputField } from '../../../gen/reliant/v1/catalog_pb'

vi.mock('../../../store/globalDataStore', () => ({
  usePresetsForWorkflow: () => ({ presets: [], loading: false }),
  useModels: () => ({ models: [], loading: false }),
  useGlobalDataStore: (sel: any) =>
    sel({ refetchPresets: vi.fn(), isInitialized: true, isPrefetching: false, models: [] }),
}))

/** Mirrors call_llm's `skills` field as the Go catalog reports it. */
const skillsField = {
  name: 'skills',
  type: 'array',
  description: '',
  required: false,
  enumValues: [],
  uiHint: '',
  label: 'Skills',
  visibilityContexts: [],
  isCel: true,
  category: 'Prompt',
} as unknown as NodeInputField

/** Stateful host mirroring WorkflowBuilder: merge the update, feed it back. */
function Host({ onStep }: { onStep?: (step: Step) => void } = {}) {
  const [step, setStep] = useState<Step>(
    { id: 'n1', type: 'call_llm', args: { case: undefined, value: undefined } } as unknown as Step,
  )
  onStep?.(step)
  return (
    <ActionStepConfig
      step={step as any}
      onUpdate={(updated) => setStep((cur) => mergeStepUpdate(cur, updated))}
      catalogNodes={[
        { id: 'call_llm', inputFields: [skillsField], outputFields: [] } as unknown as NodeInfo,
      ]}
    />
  )
}

/** Append one character at a time to what the field currently shows. */
function typeLikeAUser(label: string, text: string): string {
  for (const char of text) {
    const el = screen.getByLabelText(label) as HTMLTextAreaElement
    fireEvent.change(el, { target: { value: el.value + char } })
  }
  return (screen.getByLabelText(label) as HTMLTextAreaElement).value
}

describe('list-valued config fields', () => {
  it('displays a single typed entry', () => {
    render(<Host />)
    expect(typeLikeAUser('Skills', 'go')).toBe('go')
  })

  it('keeps the separator while a second entry is being typed', () => {
    render(<Host />)
    expect(typeLikeAUser('Skills', 'go, db')).toBe('go, db')
  })

  it('stores the typed text as a list of entries', () => {
    let latest: Step | undefined
    render(<Host onStep={(s) => { latest = s }} />)
    typeLikeAUser('Skills', 'go, db')

    const stored = getActionArgValue(latest as Step, 'skills') as {
      value?: { case?: string; value?: { values?: string[] } }
    }
    expect(stored?.value?.case).toBe('literal')
    expect(stored?.value?.value?.values).toEqual(['go', 'db'])
  })

  it('renders a pre-existing stored list back as editable text', () => {
    function Preloaded() {
      const [step] = useState<Step>({
        id: 'n1',
        type: 'call_llm',
        args: {
          case: 'callLlm',
          value: { skills: { value: { case: 'literal', value: { values: ['go', 'db'] } } } },
        },
      } as unknown as Step)
      return (
        <ActionStepConfig
          step={step as any}
          onUpdate={() => {}}
          catalogNodes={[
            { id: 'call_llm', inputFields: [skillsField], outputFields: [] } as unknown as NodeInfo,
          ]}
        />
      )
    }

    render(<Preloaded />)
    expect((screen.getByLabelText('Skills') as HTMLTextAreaElement).value).toBe('go, db')
  })
})
