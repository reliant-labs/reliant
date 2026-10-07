import { describe, expect, it, vi } from 'vitest'

/**
 * Unit tests for the machine lifecycle policy + the Restart orchestration.
 *
 * These are pure functions precisely because the interesting parts are not
 * renderable: "which actions does a FAILED machine get" is a policy table,
 * and "Restart suspends, waits for the pod to actually go away, then
 * resumes" is a sequence. Driving either one through the DOM would test
 * React's rendering of a decision rather than the decision.
 *
 * The wait step is the whole reason restartMachine exists as its own
 * function. SuspendDaemon writes status=SUSPENDED to the daemon row
 * SYNCHRONOUSLY, before the pod is torn down (control-plane
 * internal/svcdaemon/service.go SuspendDaemon: it publishes the NATS suspend
 * command and then immediately calls UpdateDaemonStatus). So a restart that
 * polled `status` would see SUSPENDED on its first read and resume into a pod
 * that is still running — which is exactly the no-op the owner is trying to
 * escape. `lifecycle_phase` is the observable that tracks the real pod: the
 * reconciler drives it SUSPENDING → SUSPENDED off the Workspace CR.
 */

import {
  LIFECYCLE_PHASE_READY,
  LIFECYCLE_PHASE_SUSPENDED,
  LIFECYCLE_PHASE_SUSPENDING,
  LIFECYCLE_PHASE_UNSPECIFIED,
  MACHINE_STATUS_ACTIVE,
  MACHINE_STATUS_DISCONNECTED,
  MACHINE_STATUS_FAILED,
  MACHINE_STATUS_PENDING,
  MACHINE_STATUS_SUSPENDED,
  lifecyclePlan,
  restartMachine,
} from '@/components/Settings/cloud/machineLifecycle'

// daemon_type arrives from the one daemon list as the string the daemon
// registered with, not a control-plane enum number (the fixtures were 1 and 2).
const managed = (status: number, lifecyclePhase = LIFECYCLE_PHASE_UNSPECIFIED) => ({
  daemonType: 'managed',
  status,
  lifecyclePhase,
})

const selfHosted = (status: number) => ({
  daemonType: 'self_hosted',
  status,
  lifecyclePhase: LIFECYCLE_PHASE_UNSPECIFIED,
})

describe('lifecyclePlan', () => {
  it('offers Suspend and Restart on a running cloud machine, both enabled', () => {
    const plan = lifecyclePlan(managed(MACHINE_STATUS_ACTIVE), null)
    expect(plan.managed).toBe(true)
    expect(plan.offer).toEqual(['suspend', 'restart'])
    expect(plan.disabledReason).toBeNull()
  })

  it('offers only Resume on a suspended cloud machine', () => {
    const plan = lifecyclePlan(managed(MACHINE_STATUS_SUSPENDED), null)
    expect(plan.offer).toEqual(['resume'])
    expect(plan.disabledReason).toBeNull()
  })

  // A wedged start (e.g. its disk is gone) lands FAILED. ResumeDaemon only
  // accepts SUSPENDED, so the way out is Suspend, then Resume. Before this the
  // plan offered only a dead Resume and the user had no action at all.
  it('offers an enabled Suspend, and an honest recovery hint, on a failed machine', () => {
    const plan = lifecyclePlan(managed(MACHINE_STATUS_FAILED), null)
    expect(plan.offer).toEqual(['suspend'])
    expect(plan.disabledReason).toBeNull()
    expect(plan.recoveryHint).toMatch(/suspend.*resume/i)
  })

  it('disables the actions with a reason while a machine is still starting', () => {
    const plan = lifecyclePlan(managed(MACHINE_STATUS_PENDING), null)
    expect(plan.offer).toEqual(['suspend', 'restart'])
    expect(plan.disabledReason).toMatch(/starting/i)
  })

  // Disconnected is not a transition — the pod is up and the daemon lost its
  // gateway connection. Restart is the single most useful thing to offer, so
  // it stays enabled rather than being lumped in with PENDING.
  it('keeps Suspend and Restart enabled on a disconnected cloud machine', () => {
    const plan = lifecyclePlan(managed(MACHINE_STATUS_DISCONNECTED, LIFECYCLE_PHASE_READY), null)
    expect(plan.offer).toEqual(['suspend', 'restart'])
    expect(plan.disabledReason).toBeNull()
  })

  // The mirror can be stale: a suspended machine whose phase never arrived
  // reads DISCONNECTED + UNSPECIFIED. Resume must be reachable.
  it('also offers Resume on an unattached cloud machine with no known phase', () => {
    const plan = lifecyclePlan(managed(MACHINE_STATUS_DISCONNECTED, LIFECYCLE_PHASE_UNSPECIFIED), null)
    expect(plan.offer).toEqual(['suspend', 'restart', 'resume'])
    expect(plan.disabledReason).toBeNull()
  })

  it('does not offer Resume on a disconnected machine whose phase is known READY', () => {
    const plan = lifecyclePlan(managed(MACHINE_STATUS_DISCONNECTED, LIFECYCLE_PHASE_READY), null)
    expect(plan.offer).toEqual(['suspend', 'restart'])
  })

  it('offers nothing for a self-hosted machine, at any status', () => {
    for (const status of [MACHINE_STATUS_ACTIVE, MACHINE_STATUS_SUSPENDED, MACHINE_STATUS_FAILED]) {
      const plan = lifecyclePlan(selfHosted(status), null)
      expect(plan.managed).toBe(false)
      expect(plan.offer).toEqual([])
    }
  })

  it('disables every action while a restart is in flight, naming the stage', () => {
    const plan = lifecyclePlan(managed(MACHINE_STATUS_ACTIVE), 'stopping')
    expect(plan.disabledReason).toMatch(/stopping/i)
  })
})

describe('restartMachine', () => {
  /** A poll script: each call returns the next phase in the list. */
  function phaseSequence(phases: number[], status = MACHINE_STATUS_SUSPENDED) {
    let i = 0
    return vi.fn(async () => ({
      phase: phases[Math.min(i++, phases.length - 1)],
      status,
    }))
  }

  it('suspends, waits for the pod to actually stop, then resumes — in that order', async () => {
    const calls: string[] = []
    const suspend = vi.fn(async () => void calls.push('suspend'))
    const resume = vi.fn(async () => void calls.push('resume'))
    // The pod is still winding down for two polls before it is really gone.
    const poll = phaseSequence([
      LIFECYCLE_PHASE_SUSPENDING,
      LIFECYCLE_PHASE_SUSPENDING,
      LIFECYCLE_PHASE_SUSPENDED,
    ])
    const stages: string[] = []

    await restartMachine({
      suspend,
      resume,
      poll,
      sleep: async () => {},
      onStage: (s) => stages.push(s),
    })

    expect(suspend).toHaveBeenCalledTimes(1)
    expect(resume).toHaveBeenCalledTimes(1)
    expect(calls).toEqual(['suspend', 'resume'])
    // It did not resume off the first read — it waited for SUSPENDED.
    expect(poll).toHaveBeenCalledTimes(3)
    expect(stages).toEqual(['stopping', 'starting'])
  })

  // The regression this function exists to prevent. Suspend marks the row
  // SUSPENDED before the pod is gone, so a restart keyed on `status` resumes
  // immediately and the pod never restarts — no new image, which is the
  // original bug.
  it('does not resume while the phase still says SUSPENDING, even though status is already SUSPENDED', async () => {
    const order: string[] = []
    const poll = vi.fn(async () => {
      order.push('poll')
      // Status flipped the moment SuspendDaemon returned; the pod has not.
      return { phase: LIFECYCLE_PHASE_SUSPENDING, status: MACHINE_STATUS_SUSPENDED }
    })

    await expect(
      restartMachine({
        suspend: async () => void order.push('suspend'),
        resume: async () => void order.push('resume'),
        poll,
        sleep: async () => {},
        onStage: () => {},
        timeoutMs: 50,
        now: (() => {
          // Walk the clock past the deadline so the test terminates.
          let t = 0
          return () => (t += 20)
        })(),
      }),
    ).rejects.toThrow(/did not stop/i)

    expect(order).not.toContain('resume')
  })

  // An older control plane sends no lifecycle_phase at all (the field is
  // UNSPECIFIED for rows that predate it). Blocking forever on a signal that
  // will never arrive would make Restart permanently broken there, so
  // UNSPECIFIED falls back to the status the server does send.
  it('falls back to status when the server reports no lifecycle phase', async () => {
    const resume = vi.fn(async () => {})
    await restartMachine({
      suspend: async () => {},
      resume,
      poll: async () => ({
        phase: LIFECYCLE_PHASE_UNSPECIFIED,
        status: MACHINE_STATUS_SUSPENDED,
      }),
      sleep: async () => {},
      onStage: () => {},
    })
    expect(resume).toHaveBeenCalledTimes(1)
  })

  const clockPastDeadline = () => {
    let t = 0
    return () => (t += 20)
  }

  it('tries resume once when the phase never reports and the wait times out', async () => {
    const resume = vi.fn(async () => {})
    await restartMachine({
      suspend: async () => {},
      resume,
      poll: async () => ({ phase: LIFECYCLE_PHASE_UNSPECIFIED, status: MACHINE_STATUS_DISCONNECTED }),
      sleep: async () => {},
      onStage: () => {},
      timeoutMs: 50,
      now: clockPastDeadline(),
    })
    expect(resume).toHaveBeenCalledTimes(1)
  })

  it('reports "still stopping" when that fallback resume is refused as not suspended', async () => {
    await expect(
      restartMachine({
        suspend: async () => {},
        resume: async () => {
          throw new Error('[failed_precondition] daemon is not suspended')
        },
        poll: async () => ({ phase: LIFECYCLE_PHASE_UNSPECIFIED, status: MACHINE_STATUS_DISCONNECTED }),
        sleep: async () => {},
        onStage: () => {},
        timeoutMs: 50,
        now: clockPastDeadline(),
      }),
    ).rejects.toThrow(/still stopping/i)
  })

  it('surfaces a suspend failure without attempting the resume', async () => {
    const resume = vi.fn(async () => {})
    await expect(
      restartMachine({
        suspend: async () => {
          throw new Error('nope')
        },
        resume,
        poll: async () => ({ phase: LIFECYCLE_PHASE_READY, status: MACHINE_STATUS_ACTIVE }),
        sleep: async () => {},
        onStage: () => {},
      }),
    ).rejects.toThrow('nope')
    expect(resume).not.toHaveBeenCalled()
  })
})
