import type { Step, Workflow } from '../types/workflow'

function getOneofString(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  if (!value || typeof value !== 'object') return undefined

  const recordValue = value as {
    expr?: unknown
    literal?: unknown
    value?: unknown
  }

  if (typeof recordValue.expr === 'string') return recordValue.expr
  if (typeof recordValue.literal === 'string') return recordValue.literal

  if (recordValue.value && typeof recordValue.value === 'object') {
    const oneofValue = recordValue.value as { value?: unknown }
    if (typeof oneofValue.value === 'string') {
      return oneofValue.value
    }
  }

  return undefined
}

export function getSubWorkflowRef(step: Step): string | undefined {
  const flattened = getOneofString(step.ref)
  if (flattened) return flattened

  if (step.args?.case === 'workflow') {
    return getOneofString(step.args.value.ref)
  }

  if (step.args?.case === 'loop') {
    return getOneofString(step.args.value.ref)
  }

  return undefined
}

export function getSubWorkflowInline(step: Step): Workflow | undefined {
  if (step.inline) return step.inline

  if (step.args?.case === 'workflow' && step.args.value.inline) {
    return step.args.value.inline as Workflow
  }

  if (step.args?.case === 'loop' && step.args.value.inline) {
    return step.args.value.inline as Workflow
  }

  return undefined
}

export function getLoopRef(step: Step): string | undefined {
  if (step.args?.case === 'loop') {
    return getSubWorkflowRef(step)
  }

  const flattened = getOneofString(step.ref)
  return flattened
}

export function getLoopInline(step: Step): Workflow | undefined {
  if (step.args?.case === 'loop') {
    return getSubWorkflowInline(step)
  }

  return step.inline
}

export function getLoopWhile(step: Step): string | undefined {
  const flattened = getOneofString(step.while)
  if (flattened) return flattened

  if (step.args?.case === 'loop') {
    const whileExpr = step.args.value.while
    if (whileExpr && typeof whileExpr === 'object' && 'expr' in whileExpr) {
      const expr = (whileExpr as { expr?: unknown }).expr
      if (typeof expr === 'string') {
        return expr
      }
    }
  }

  return undefined
}
