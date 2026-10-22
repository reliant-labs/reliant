import { useCatalogModelName } from "../../hooks/useCatalogModelName";
import { splitModelId } from "../../lib/modelId";

interface ComposerModelNameProps {
  /** Name of the model as resolved from the user's model list, when it is there. */
  name?: string;
  /** The raw pin ("gpt-5.6-sol@codex"), when the composer has one. */
  pinnedId?: string;
}

/**
 * The model pill's label. A pin the user's model list cannot name — its
 * provider is not connected — is named from that provider's catalog, and
 * never shown as the raw "<model>@<driver>" id.
 */
export function ComposerModelName({ name, pinnedId }: ComposerModelNameProps) {
  const catalogName = useCatalogModelName(name ? undefined : pinnedId);
  if (name) return <>{name}</>;
  if (!pinnedId) return <>auto</>;
  return <>{catalogName ?? splitModelId(pinnedId).modelId}</>;
}
