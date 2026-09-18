/**
 * useForgeProject answers "should this project show the forge screens?".
 *
 * IT DELIBERATELY DOES NOT READ `Project.is_forge`. That column is populated at
 * clone / project-create time and never recomputed on read — the proto says so
 * — which makes it stale-false for any project row that predates its forge.yaml
 * or gained one later. Verified against the real dev database: the
 * `control-plane` row carries is_forge = false while control-plane/forge.yaml
 * exists on disk. Gating on that flag would hide the forge screens from the very
 * project they were built against, and the failure is silent — a missing nav
 * entry looks like a feature that was never built.
 *
 * The authority is instead `ForgeReportMeta.isForgeProject`, which the daemon
 * derives by statting forge.yaml at request time. That is a LIVE answer about
 * the filesystem as it is now, and it is the same answer every forge screen
 * already renders from, so the nav entry and the screens can never disagree.
 *
 * Cost is nil: this shares react-query's cache with the topology screen's own
 * `useForgeTopology`, so the nav entry and the screen issue ONE request between
 * them.
 *
 * UNKNOWN IS NOT FALSE. While the query is in flight, or when it failed for a
 * transport reason, `isForgeProject` is false and `isDetermined` is false. A
 * caller that wants to avoid flashing a nav entry in and out should branch on
 * `isDetermined`; a caller that just wants "show it when we know" can use
 * `isForgeProject` alone. The two are kept separate rather than collapsed
 * because "no forge.yaml here" and "we have not looked yet" are different facts,
 * and this feature's whole design rests on not conflating them.
 */
import { useForgeTopology } from "./forge-queries";

export interface ForgeProjectGate {
  /** True only when the daemon confirmed a forge.yaml at the project path. */
  isForgeProject: boolean;
  /** True once an answer arrived — success or a definitive non-forge verdict. */
  isDetermined: boolean;
}

export function useForgeProject(projectId: string | null | undefined): ForgeProjectGate {
  const { data, isSuccess } = useForgeTopology(projectId);

  // `not-forge-project` is the one outcome that is a definitive NO. Every other
  // outcome — report, unsupported, unreachable, malformed — means a forge.yaml
  // WAS found, so the screens have something to say (even if that something is
  // "your forge is too old"). Hiding the entry on `unsupported` would leave the
  // user with no way to discover why.
  const isForgeProject = isSuccess && !!data && data.kind !== "not-forge-project";

  return { isForgeProject, isDetermined: isSuccess };
}
