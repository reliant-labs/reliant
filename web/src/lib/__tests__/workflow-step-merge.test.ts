import { celString } from "../celAdapter"
import { mergeStepUpdate, withRouterArgs, type Step } from "../../types/workflow"

describe("mergeStepUpdate", () => {
  it("preserves existing router candidates when a stale update only changes system prompt", () => {
    const currentStep: Step = {
      id: "router_1",
      type: "router",
      args: {
        case: "router",
        value: {
          workflows: [
            {
              ref: "builtin://agent",
              presets: ["fast"],
              description: "Primary candidate",
            },
          ],
          fallback: "workflow://fallback",
        },
      } as Step["args"],
    }

    const staleUpdatedStep: Step = {
      id: "router_1",
      type: "router",
      args: {
        case: "router",
        value: {
          systemPrompt: celString("Route carefully"),
        },
      } as Step["args"],
    }

    const merged = mergeStepUpdate(currentStep, staleUpdatedStep)
    const mergedArgs = merged.args?.value as Record<string, unknown>

    expect(mergedArgs.workflows).toEqual([
      {
        ref: "builtin://agent",
        presets: ["fast"],
        description: "Primary candidate",
      },
    ])
    expect(mergedArgs.fallback).toBe("workflow://fallback")
    expect(mergedArgs.systemPrompt).toEqual(celString("Route carefully"))
  })

  it("replaces args when the step arg case changes", () => {
    const currentStep: Step = {
      id: "step_1",
      type: "workflow",
      args: {
        case: "workflow",
        value: {
          ref: celString("builtin://agent"),
          args: { message: "hello" },
        },
      } as Step["args"],
    }

    const updatedStep: Step = {
      id: "step_1",
      type: "workflow",
      args: {
        case: "loop",
        value: {
          while: { expr: "true" },
        },
      } as Step["args"],
    }

    expect(mergeStepUpdate(currentStep, updatedStep)).toEqual(updatedStep)
  })

  it("documents that stale withRouterArgs spreads empty workflows over current state", () => {
    // This reproduces the actual bug: a stale step closure in MonacoCELEditor's
    // onChange captures the initial step (with workflows: []), and withRouterArgs
    // spreads that empty array into the update, overwriting the real candidates.
    const staleStep: Step = {
      id: "router_1",
      type: "router",
      args: {
        case: "router",
        value: {
          workflows: [],
        },
      } as Step["args"],
    }

    // withRouterArgs spreads staleStep.args.value (which has workflows: []) then overlays the update
    const updatedStep = withRouterArgs(staleStep, { systemPrompt: celString("Route carefully") })
    const updatedArgs = updatedStep.args?.value as Record<string, unknown>
    // The stale update explicitly contains workflows: []
    expect(updatedArgs.workflows).toEqual([])

    // The current (real) state has candidates
    const currentStep: Step = {
      id: "router_1",
      type: "router",
      args: {
        case: "router",
        value: {
          workflows: [
            { ref: "builtin://agent", presets: ["fast"], description: "Primary" },
          ],
          fallback: "default-fallback",
        },
      } as Step["args"],
    }

    // mergeStepUpdate shallow-merges: the stale workflows: [] overwrites the real candidates
    const merged = mergeStepUpdate(currentStep, updatedStep)
    const mergedArgs = merged.args?.value as Record<string, unknown>

    // With the fix (onChangeRef in MonacoCELEditor), the onChange callback is always fresh,
    // so withRouterArgs uses the current step with real candidates. This test documents
    // the shallow-merge limitation: if the update contains an explicit workflows: [],
    // it WILL overwrite the current candidates. The fix is at the callback layer.
    expect(mergedArgs.workflows).toEqual([])
    expect(mergedArgs.systemPrompt).toEqual(celString("Route carefully"))
    expect(mergedArgs.fallback).toBe("default-fallback")
  })
})