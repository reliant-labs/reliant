/**
 * Whether a person's daemons include one they can use: the one predicate the
 * wizard's machine step (steps/ComputeStep.tsx), the returning-user heal
 * (useReturningUserHeal.ts) and the shell's "do chats have a machine?"
 * (onboardingGate.ts, chatSetup) all read. Its own module so the shell can
 * import it without the wizard's step.
 */

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";

// The "where do you want to run your daemon?" step is bootstrap-only: it
// disambiguates a brand-new user between "Reliant Cloud" and "your own
// machine." Once a daemon — local OR managed — is registered to the user's
// account, that bootstrap question is moot. The post-onboarding workspace
// picker handles selection when multiple daemons exist.
//
// "Usable" means the user has clearly already picked a daemon location, so
// the where-question must not re-appear:
//   - ACTIVE:    actively attached (the obvious case).
//   - PENDING:   a managed machine that is provisioning or cloning. The user
//                asked for it; it is coming.
//   - SUSPENDED: a real machine, deliberately parked, which resumes on use.
// DISCONNECTED / FAILED / UNSPECIFIED are skipped: a stale row, a machine
// that will never come up, or an unhealthy registration — the cases where the
// user genuinely needs to (re)decide.
//
// There used to be TWO of these predicates, one per DaemonStatus enum, because
// the UI read two different daemon lists whose numeric values collided
// (reliant ACTIVE=1 was control-plane PENDING=1; reliant IDLE=2 was
// control-plane ACTIVE=2). There is now one list and one enum, so there is one
// predicate — and PENDING and SUSPENDED are nameable here for the first time,
// which is why this reads richer than the registry half it replaces rather
// than narrower than the control-plane half. See
// docs/design/one-daemon-list.md.
export function hasUsableDaemonForOnboarding(
  daemons: ReadonlyArray<Pick<DaemonInfo, "status">>,
): boolean {
  return daemons.some(
    (d) =>
      d.status === DaemonStatus.ACTIVE ||
      d.status === DaemonStatus.PENDING ||
      d.status === DaemonStatus.SUSPENDED,
  );
}
