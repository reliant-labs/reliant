/**
 * Machine lifecycle policy and the Restart sequence.
 *
 * Split out of machines.tsx because neither of these is presentation. The
 * policy is a table ("which actions does a FAILED cloud machine get, and is
 * Resume clickable"), and Restart is a sequence with a wait in the middle;
 * both are worth testing without a DOM, and the wait in particular encodes a
 * control-plane fact that is easy to get wrong.
 *
 * ── Why Restart is client-side, and what that costs ─────────────────────────
 *
 * There is no RestartDaemon RPC. The control plane exposes SuspendDaemon and
 * ResumeDaemon (control-plane proto/services/daemon/v1/daemon.proto) and
 * nothing that composes them, so "restart" is this client driving the two in
 * order. That is adequate here but it is NOT free of caveats — see the note
 * at the bottom of this comment and the report accompanying this change.
 *
 * The important asymmetry, read off control-plane
 * internal/svcdaemon/service.go:
 *
 *   SuspendDaemon  publishes a NATS suspend command, then IMMEDIATELY calls
 *                  UpdateDaemonStatus(..., SUSPENDED) and returns. The pod is
 *                  still running when the RPC returns. The CommandBridge
 *                  handles the command asynchronously (handleSuspend patches
 *                  the Workspace CR to state=Suspended) and the reconciler
 *                  tears the pod down afterwards.
 *
 *   ResumeDaemon   refuses anything whose status is not SUSPENDED
 *                  (CodeFailedPrecondition "daemon is not suspended"), and on
 *                  success re-stamps the desired image + size-derived
 *                  resources from the CURRENT config onto the row, publishing
 *                  them on the resume command so the bridge converges the CR
 *                  spec (handleResume).
 *
 * So resume is the rollout point — exactly the property this feature is for.
 * Prod leaves ROLLOUT_STRATEGY unset, which derives to RolloutStrategyResume
 * ("never force-roll a running pod; the new image lands when the pod is next
 * rebuilt from spec"). A running pod therefore keeps its old workspace image
 * indefinitely, and suspend→resume is the supported way to pick up a new one.
 *
 * And it is why polling `status` would be WRONG. Status is SUSPENDED the
 * instant SuspendDaemon returns, so a restart that waited on status would
 * resume while the old pod was still up. ResumeDaemon would accept it (status
 * does say SUSPENDED), the bridge would patch the CR back to Running before
 * the suspend had taken effect, and the pod would never be rebuilt — no new
 * image, which is the original bug with extra steps.
 *
 * `lifecycle_phase` is the observable that tracks the real pod. It is mapped
 * from the Workspace CR's phase, not from the daemon row
 * (internal/svcdaemon/convert.go, and phaseToStatus in
 * workspace_state_reconciler.go), and it goes SUSPENDING → SUSPENDED as the
 * pod actually goes away. That is what this waits for.
 *
 * Residual caveat, stated plainly: this sequence is not atomic. If the tab
 * closes between suspend and resume, the machine is left suspended — safe,
 * visible, and resumable from the same page, but not what the user asked
 * for. A server-side RestartDaemon RPC would fix that and would also be able
 * to wait on the CR directly instead of polling. It is the better design if
 * restart becomes load-bearing; it is not required for correctness of the
 * common case, which is why this ships without blocking on a proto change.
 */

import {
  DaemonLifecyclePhase,
  DaemonStatus,
} from '@/gen/reliant/v1/daemon_registry_pb'

// Named re-exports of the REGISTRY enums, taken from the generated source
// rather than retyped, so a renumbered proto cannot leave a stale literal
// behind here.
//
// These pointed at control-plane's enums until the daemon list was
// consolidated (docs/design/one-daemon-list.md). There were two DaemonStatus
// enums whose numeric values COLLIDED — 3 was SUSPENDED in control-plane and
// DISCONNECTED in the registry — and the MACHINE_ prefix existed so no call
// site had to remember which one it was holding. There is now one list and one
// enum; the prefix stays because it still reads better at the call sites, not
// because there is an ambiguity left to guard.
export const MACHINE_STATUS_PENDING = DaemonStatus.PENDING
export const MACHINE_STATUS_ACTIVE = DaemonStatus.ACTIVE
export const MACHINE_STATUS_SUSPENDED = DaemonStatus.SUSPENDED
export const MACHINE_STATUS_DISCONNECTED = DaemonStatus.DISCONNECTED
export const MACHINE_STATUS_FAILED = DaemonStatus.FAILED
export const MACHINE_STATUS_UNKNOWN = DaemonStatus.UNSPECIFIED

export const LIFECYCLE_PHASE_UNSPECIFIED = DaemonLifecyclePhase.UNSPECIFIED
export const LIFECYCLE_PHASE_READY = DaemonLifecyclePhase.READY
export const LIFECYCLE_PHASE_SUSPENDING = DaemonLifecyclePhase.SUSPENDING
export const LIFECYCLE_PHASE_SUSPENDED = DaemonLifecyclePhase.SUSPENDED
export const LIFECYCLE_PHASE_FAILED = DaemonLifecyclePhase.FAILED

// The registry carries daemon_type as the string the daemon registered with,
// not an enum, so the comparison is by name. "self_hosted" is what
// tools_daemon.go records; "external" is control-plane's word for the same
// thing and is accepted so a row back-filled from its vocabulary still reads
// as unmanaged rather than silently being offered suspend/resume it cannot do.
const EXTERNAL_DAEMON_TYPES = ['self_hosted', 'external']

/**
 * The stages a Restart passes through, for progress display. `resizing` only
 * occurs when the restart carries a resize (see restartMachine's `resize`).
 */
export type RestartStage = 'stopping' | 'resizing' | 'starting'

/**
 * `resize` changes the machine size. The control plane only resizes a
 * SUSPENDED machine (control-plane docs/design/daemon-resize.md), so on a
 * suspended machine it is one call that takes effect at the next start, and
 * on a running one it is a restart with the resize done while stopped.
 */
export type LifecycleAction = 'suspend' | 'resume' | 'restart' | 'resize'

const STAGE_REASON: Record<RestartStage, string> = {
  stopping: 'Stopping the machine…',
  resizing: 'Applying the new size…',
  starting: 'Starting the machine…',
}

export interface LifecyclePlan {
  /** False for self-hosted machines: nothing here can control them. */
  managed: boolean
  /** Which actions to render, in order. Empty for self-hosted. */
  offer: LifecycleAction[]
  /**
   * Why the offered actions are unclickable, or null when they are live.
   * Doubles as the button's `title`, so the reason is always reachable
   * rather than being implied by a greyed-out control.
   */
  disabledReason: string | null
  /**
   * Plain-language next step shown beside the failure reason, or null.
   * Set for a FAILED machine, whose way back is Suspend then Resume.
   */
  recoveryHint: string | null
}

/** The subset of a Daemon this policy reads. */
interface MachineLike {
  daemonType: string
  status: number
  lifecyclePhase?: number
}

/**
 * What lifecycle actions does this machine get, and are they enabled?
 *
 * `restartStage` is non-null while this client is mid-restart; it disables
 * everything and names the stage, because the machine is being driven
 * through two RPCs and a second command would interleave with them.
 */
export function lifecyclePlan(
  machine: MachineLike,
  restartStage: RestartStage | null,
): LifecyclePlan {
  if (EXTERNAL_DAEMON_TYPES.includes(machine.daemonType)) {
    return { managed: false, offer: [], disabledReason: null, recoveryHint: null }
  }

  if (restartStage) {
    return {
      managed: true,
      offer:
        machine.status === MACHINE_STATUS_SUSPENDED
          ? ['resume', 'resize']
          : ['suspend', 'restart', 'resize'],
      disabledReason: STAGE_REASON[restartStage],
      recoveryHint: null,
    }
  }

  // Resize is offered wherever Restart or Resume is, because it IS one of
  // them with a step in the middle: a suspended machine is resized in place,
  // a running one is stopped, resized and started. A FAILED machine gets
  // neither — its way back is Suspend, after which Resize is offered.
  switch (machine.status) {
    case MACHINE_STATUS_SUSPENDED:
      return { managed: true, offer: ['resume', 'resize'], disabledReason: null, recoveryHint: null }

    // ResumeDaemon only accepts a SUSPENDED daemon, so Resume is not offered
    // here — it would be a button whose single outcome is an error.
    // SuspendDaemon DOES accept a failed machine (control-plane
    // TestSuspendDaemon_FromFailedIsAllowed): it moves the Workspace to
    // Suspended, the pod is torn down, and Resume then retries the start.
    // That is the user's way out of a wedged start (a missing disk, an
    // attach that never completes). Restart is deliberately NOT offered: it
    // aborts when it sees FAILED while waiting for the stop.
    case MACHINE_STATUS_FAILED:
      return {
        managed: true,
        offer: ['suspend'],
        disabledReason: null,
        recoveryHint:
          'To try again, suspend this machine, then resume it. If it fails the same way, contact support.',
      }

    // Mid-transition. Suspending a machine that is still coming up races the
    // reconciler, so wait for it to settle.
    case MACHINE_STATUS_PENDING:
      return {
        managed: true,
        offer: ['suspend', 'restart', 'resize'],
        disabledReason: 'This machine is still starting.',
        recoveryHint: null,
      }

    // Unknown status (UNSPECIFIED): a managed machine whose lifecycle mirror
    // is missing, so it may be suspended. Offer everything the server will
    // vet: it refuses Resume unless the machine is really suspended.
    case MACHINE_STATUS_UNKNOWN:
      return {
        managed: true,
        offer: ['suspend', 'restart', 'resume', 'resize'],
        disabledReason: null,
        recoveryHint: null,
      }

    // ACTIVE, and DISCONNECTED — which is not a transition: the pod is up and
    // the daemon lost its gateway connection. Restart is the most useful
    // thing to offer there, so it stays live.
    //
    // Unattached with NO phase is different: the lifecycle mirror never
    // landed (or was lost), so the machine may in truth be suspended and
    // DISCONNECTED is only a guess. Resume is offered too so a stale mirror
    // can never leave a machine with no way to start; the server refuses it
    // with FailedPrecondition if the machine is not actually suspended.
    default:
      if (
        machine.status === MACHINE_STATUS_DISCONNECTED &&
        (machine.lifecyclePhase ?? LIFECYCLE_PHASE_UNSPECIFIED) === LIFECYCLE_PHASE_UNSPECIFIED
      ) {
        return {
          managed: true,
          offer: ['suspend', 'restart', 'resume', 'resize'],
          disabledReason: null,
          recoveryHint: null,
        }
      }
      return { managed: true, offer: ['suspend', 'restart', 'resize'], disabledReason: null, recoveryHint: null }
  }
}

export interface RestartOptions {
  suspend: () => Promise<void>
  resume: () => Promise<void>
  /** Reads the machine's current phase + status from the server. */
  poll: () => Promise<{ phase: number; status: number }>
  sleep: (ms: number) => Promise<void>
  onStage: (stage: RestartStage) => void
  /**
   * Optional step run once the machine has stopped and before it starts
   * again — how a RUNNING machine is resized, since the control plane only
   * resizes a suspended one. The machine is started again whether or not
   * this succeeds (on its old size if it failed), and its error is then
   * rethrown: a refused resize must never leave a machine the user had
   * running switched off.
   */
  resize?: () => Promise<void>
  /** Give up waiting for the pod to stop after this long. */
  timeoutMs?: number
  pollIntervalMs?: number
  /** Injectable clock, so the timeout is testable without real time. */
  now?: () => number
}

const DEFAULT_RESTART_TIMEOUT_MS = 120_000
const DEFAULT_POLL_INTERVAL_MS = 2_000

/**
 * Suspend, wait until the pod is really gone, then resume.
 *
 * The wait is the point: see this module's header for why `status` is not a
 * usable signal and `lifecycle_phase` is. Throws if the machine does not
 * reach a stopped phase within the timeout, rather than resuming anyway —
 * resuming early is the failure mode that silently produces no restart at
 * all.
 */
export async function restartMachine(opts: RestartOptions): Promise<void> {
  const {
    suspend,
    resume,
    poll,
    sleep,
    onStage,
    resize,
    timeoutMs = DEFAULT_RESTART_TIMEOUT_MS,
    pollIntervalMs = DEFAULT_POLL_INTERVAL_MS,
    now = () => Date.now(),
  } = opts

  // The second half of the sequence, once the machine is believed stopped.
  // The resize step's failure is held until AFTER the resume, so the machine
  // comes back either way. A resume failure wins over it: then the machine
  // is left stopped, which is the more urgent thing to tell the user.
  // `onResumeError` lets the stale-mirror path below reword a resume refusal
  // without also catching the resize step's error.
  const startAgain = async (onResumeError?: (e: unknown) => never) => {
    let resizeError: unknown
    if (resize) {
      onStage('resizing')
      try {
        await resize()
      } catch (e) {
        resizeError = e
      }
    }
    onStage('starting')
    try {
      await resume()
    } catch (e) {
      if (onResumeError) onResumeError(e)
      throw e
    }
    if (resizeError !== undefined) throw resizeError
  }

  onStage('stopping')
  await suspend()

  const deadline = now() + timeoutMs
  let sawKnownPhase = false
  for (;;) {
    const { phase, status } = await poll()
    if (phase !== LIFECYCLE_PHASE_UNSPECIFIED) sawKnownPhase = true

    if (phase === LIFECYCLE_PHASE_SUSPENDED) break

    // An older control plane sends no phase at all (UNSPECIFIED for rows
    // predating the field). Blocking on a signal that will never arrive
    // would make Restart permanently broken there, so fall back to the
    // status the server does send. Less precise — this is the read that can
    // be true before the pod is gone — but it is the only signal available,
    // and it still beats refusing to restart.
    if (phase === LIFECYCLE_PHASE_UNSPECIFIED && status === MACHINE_STATUS_SUSPENDED) break

    // A machine that fell over while stopping will not reach SUSPENDED.
    // Stop waiting and say so rather than spinning to the timeout.
    if (phase === LIFECYCLE_PHASE_FAILED || status === MACHINE_STATUS_FAILED) {
      throw new Error('The machine failed while stopping. It was not restarted.')
    }

    if (now() >= deadline) {
      // The phase never reported anything: the mirror is stale, so "no
      // SUSPENDED seen" proves nothing (the machine may have been suspended
      // all along). Try the resume once; the server refuses it unless the
      // machine really is suspended, which is the check we cannot make here.
      if (!sawKnownPhase) {
        await startAgain((e) => {
          const message = e instanceof Error ? e.message : String(e)
          if (/not suspended|failed.?precondition/i.test(message)) {
            throw new Error(
              'The machine is still stopping. Try Resume again in a moment.',
            )
          }
          throw e
        })
        return
      }
      throw new Error(
        'The machine did not stop in time, so it was not restarted. It may still be stopping — check its status and resume it when it is suspended.',
      )
    }

    await sleep(pollIntervalMs)
  }

  await startAgain()
}
