/**
 * Whether the forge console lists Experiments (Forge Changes: remote config,
 * gradual rollouts and A/B tests) in its nav.
 *
 * OFF BY DEFAULT, from the deployment's web config (`CHANGES_UI = "true"`,
 * web_config.proto). The page talks to the control plane's ChangesService,
 * which is not served yet; a nav row that opened onto "not available" would
 * be a promise the product cannot keep. The route itself stays mounted (a
 * URL still opens and says so), so turning this on is the only step once the
 * control plane ships.
 *
 * A deployment flag rather than a per-browser one (lib/forgeFeature.ts): it
 * tracks what the deployment's control plane can do, not a person's taste.
 */
import { loadConfig } from "@/lib/config_gen";

export function changesUIEnabled(): boolean {
  return loadConfig().CHANGES_UI === "true";
}
