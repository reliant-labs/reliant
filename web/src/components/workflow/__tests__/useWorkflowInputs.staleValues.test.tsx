import { describe, expect, it, vi, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { toJson } from '@bufbuild/protobuf'
import { ValueSchema } from '@bufbuild/protobuf/wkt'

/**
 * Reproduces the workflow-builder typing bug.
 *
 * Selecting `builtin://structured-agent` on a workflow node renders a Response
 * Tool editor. Editing its Description calls `handleResponseToolChange`, which
 * fires THREE `handleInputChange` calls in a row inside one event handler:
 *
 *   handleInputChange("response_tool_name", ...)
 *   handleInputChange("response_tool_description", ...)
 *   handleInputChange("response_schema", ...)
 *
 * `handleInputChange` builds its next state from the `values` prop captured in
 * its `useCallback` closure. React has not re-rendered between the three calls,
 * so all three start from the SAME stale `values` and each one overwrites the
 * previous. Only the last write survives — the typed description is discarded,
 * the field re-renders with its old value, and typing appears to do nothing
 * even though the caret is placed correctly.
 *
 * The fix is to make the update functional so each call composes onto the
 * result of the previous one rather than onto a stale snapshot.
 */

vi.mock('../../../api/workflow-grpc', () => ({
  workflowGrpc: {
    listWorkflows: vi.fn(async () => []),
    getWorkflow: vi.fn(async () => ({ workflow: null })),
  },
}))

vi.mock('../../../api/preset-grpc', () => ({
  presetGrpc: { getDefaultPresets: vi.fn(async () => ({})) },
}))

vi.mock('../../../store/globalDataStore', () => ({
  usePresetsForWorkflow: () => ({ presets: [], loading: false }),
}))

vi.mock('../../../store/preferencesStore', () => ({
  usePreferencesStore: (selector: (s: unknown) => unknown) =>
    selector({ isPresetHidden: () => false }),
}))

import { useWorkflowInputs } from '../useWorkflowInputs'

/** Read a stored proto Value back as plain JSON. */
function readBack(values: Record<string, unknown>, name: string): unknown {
  const raw = values[name]
  if (raw === undefined) return undefined
  return toJson(ValueSchema, raw as never)
}

/**
 * Drives the hook the way WorkflowStepConfig does: `values` is owned by the
 * caller and fed back in on each render, so the hook sees real React state.
 */
function renderInputs() {
  let current: Record<string, unknown> = {}

  const view = renderHook(
    ({ values }: { values: Record<string, unknown> }) =>
      useWorkflowInputs({
        projectId: 'project-1',
        workflowRef: 'builtin://structured-agent',
        values,
        onValuesChange: (next) => {
          current = next
          view.rerender({ values: next })
        },
        enabled: true,
      }),
    { initialProps: { values: current } },
  )

  return {
    get values() {
      return current
    },
    handleInputChange: (name: string, value: unknown) =>
      view.result.current.handleInputChange(name, value),
  }
}

describe('useWorkflowInputs — batched input changes', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('keeps every field when three inputs change in one handler', async () => {
    const inputs = renderInputs()

    // Exactly what handleResponseToolChange does when the user edits the
    // Response Tool description: three writes, one event handler, no render
    // in between.
    await act(async () => {
      inputs.handleInputChange('response_tool_name', 'submit_response')
      inputs.handleInputChange('response_tool_description', 'My description')
      inputs.handleInputChange('response_schema', { type: 'object' })
    })

    expect(readBack(inputs.values, 'response_tool_name')).toBe('submit_response')
    expect(readBack(inputs.values, 'response_tool_description')).toBe('My description')
    expect(readBack(inputs.values, 'response_schema')).toEqual({ type: 'object' })
  })

  it('preserves earlier fields when a later write lands in the same tick', async () => {
    const inputs = renderInputs()

    await act(async () => {
      inputs.handleInputChange('model', 'claude-sonnet-5')
    })
    expect(readBack(inputs.values, 'model')).toBe('claude-sonnet-5')

    // A second batch must not wipe the value written by the first.
    await act(async () => {
      inputs.handleInputChange('response_tool_name', 'submit_response')
      inputs.handleInputChange('response_tool_description', 'Typed text')
    })

    expect(readBack(inputs.values, 'model')).toBe('claude-sonnet-5')
    expect(readBack(inputs.values, 'response_tool_name')).toBe('submit_response')
    expect(readBack(inputs.values, 'response_tool_description')).toBe('Typed text')
  })
})
