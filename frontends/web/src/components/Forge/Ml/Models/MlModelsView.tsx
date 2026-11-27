// Copyright (c) 2026 Reliant Labs

/**
 * ML › Models (02-views §5). Three pages:
 *
 *   models                     the registry: every model, its aliases, the
 *                              candidate, serving and the last gate
 *   models/<model>             versions, lineage, the candidate's scorecard,
 *                              promotion gates per env, alias history
 *   models/<model>/<version>   model card, files, gate, where it serves,
 *                              serving and pull performance, lineage
 */

import { ModelPage } from "./ModelPage";
import { RegistryPage } from "./RegistryPage";
import { VersionPage } from "./VersionPage";

export function MlModelsView({ sub }: { sub: readonly string[] }) {
  if (sub[0] && sub[1]) return <VersionPage modelId={sub[0]} versionId={`${sub[0]}/${sub.slice(1).join("/")}`} />;
  if (sub[0]) return <ModelPage modelId={sub[0]} />;
  return <RegistryPage />;
}
