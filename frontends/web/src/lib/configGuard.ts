// MUST BE IMPORTED SECOND in main.tsx, right after ./browser-log-boot, and
// as an import rather than a call: imports evaluate in order, before any of
// main.tsx's own statements. Modules further down the graph read the runtime
// config at module scope (services/controlPlane/config.ts picks the cloud or
// self-hosted implementations from it), so a check run from main.tsx's body
// would come after they had already acted on a broken document.
//
// When the document cannot work this renders what is wrong and THROWS, which
// stops the module graph: no later module evaluates, so the app never boots
// half-configured behind the message. See checkWebConfig in configReady.ts.
import { checkWebConfig, renderConfigError } from "./configReady";

const problems = checkWebConfig();
if (problems.length > 0) {
  renderConfigError(problems);
  throw new Error(`[config] invalid runtime configuration:\n  - ${problems.join("\n  - ")}`);
}
