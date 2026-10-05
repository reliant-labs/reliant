// Copyright (c) 2025 Reliant Labs

/**
 * Config for an integration `action` node (`type: action`, `uses: <ref>`):
 * the form comes from GetCatalogEntry(ref).params_schema, rendered field by
 * field through ProtoFieldRenderer — the same renderer and CEL toggle every
 * built-in node uses. No per-integration components
 * (research/INTEGRATIONS_V1_BRIEF.md §3a).
 *
 * Above the form: the connection picker and, for an action that writes, a
 * "Changes data in GitHub" badge so the side effect is visible while
 * authoring. Every edit goes back through the step model (onUpdate), so the
 * canonical YAML is the server's to write.
 */

import { useState } from "react";
import { AlertTriangle } from "lucide-react";

import Badge from "../../forge-ui/badge";
import type { Step } from "../../../types/workflow";
import { ProtoFieldRenderer } from "../ProtoFieldRenderer";
import { ConnectionPicker } from "../connections/ConnectionPicker";
import { ConnectIntegrationDialog } from "../connections/ConnectIntegrationDialog";
import { IntegrationIcon } from "../palette/IntegrationIcon";
import { useCatalogEntry } from "../../../hooks/connection-queries";
import {
  getActionConnection,
  getActionParams,
  getActionUses,
  withActionConnection,
  withActionParam,
} from "../../../lib/actionNodeArgs";
import {
  actionParamFields,
  fieldValueToParam,
  missingRequiredParams,
  paramToFieldValue,
} from "../../../lib/jsonSchemaFields";
import { refIntegration } from "../../../api/catalog-search-grpc";
import { Section, SectionFields, SectionLabel } from "./primitives";

export interface IntegrationActionConfigProps {
  step: Step;
  onUpdate: (step: Step) => void;
  isReadOnly?: boolean;
}

export function IntegrationActionConfig({ step, onUpdate, isReadOnly = false }: IntegrationActionConfigProps) {
  const uses = getActionUses(step);
  const entryQuery = useCatalogEntry(uses || undefined);
  const [connectOpen, setConnectOpen] = useState(false);

  if (!uses) {
    return (
      <Section>
        <p className="cpv2-field-hint !mt-0">
          This action doesn't name what to run. Set <code className="font-mono">uses:</code> in the YAML, or delete this step and add an
          action from the step palette.
        </p>
      </Section>
    );
  }

  if (uses.includes("{{")) {
    return (
      <Section>
        <p className="cpv2-field-hint !mt-0">
          This action is chosen at run time (<code className="font-mono">{uses}</code>), so its form can't be shown. Edit its parameters in the YAML.
        </p>
      </Section>
    );
  }

  if (entryQuery.isLoading) {
    return (
      <Section>
        <div role="status" aria-label="Loading action" className="space-y-2">
          <div className="h-3 w-1/2 animate-pulse rounded bg-muted motion-reduce:animate-none" />
          <div className="h-8 animate-pulse rounded bg-muted motion-reduce:animate-none" />
          <div className="h-8 animate-pulse rounded bg-muted motion-reduce:animate-none" />
        </div>
      </Section>
    );
  }

  if (entryQuery.isError || !entryQuery.data) {
    return (
      <Section>
        <div role="alert" className="flex items-start gap-2 text-sm">
          <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0 text-warning-ink" aria-hidden />
          <div className="space-y-1">
            <p className="text-foreground">
              <code className="font-mono">{uses}</code> isn't in the integration catalog.
            </p>
            <p className="cpv2-field-hint !mt-0">It may have been renamed or removed, or its integration isn't installed here.</p>
            <button type="button" onClick={() => void entryQuery.refetch()} className="text-xs font-medium text-primary hover:underline">
              Retry
            </button>
          </div>
        </div>
      </Section>
    );
  }

  const entry = entryQuery.data;
  const integration = entry.summary.integration;
  const fields = actionParamFields(entry.paramsSchema);
  const params = getActionParams(step);
  const missing = missingRequiredParams(fields, params);

  return (
    <>
      <Section>
        <div className="flex items-start gap-3">
          <IntegrationIcon hint={integration.icon || refIntegration(uses)} />
          <div className="min-w-0 flex-1 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm font-semibold text-foreground">{entry.summary.displayName}</span>
              <span className="text-xs text-muted-foreground">{integration.displayName}</span>
            </div>
            <p className="cpv2-field-hint !mt-0">{entry.description || entry.summary.summary}</p>
            {entry.summary.mutates && <Badge label={`Changes data in ${integration.displayName}`} variant="warning" size="sm" dot />}
          </div>
        </div>
      </Section>

      <Section>
        <ConnectionPicker
          entry={entry}
          value={getActionConnection(step)}
          onChange={(connectionId) => onUpdate(withActionConnection(step, connectionId))}
          onConnect={() => setConnectOpen(true)}
          disabled={isReadOnly}
        />
      </Section>

      <Section>
        <SectionLabel>Parameters</SectionLabel>
        {fields.length === 0 ? (
          <p className="cpv2-field-hint !mt-0 italic">This action takes no parameters.</p>
        ) : (
          <SectionFields>
            {fields.map((field) => (
              <ProtoFieldRenderer
                key={field.name}
                schema={field.schema}
                value={paramToFieldValue(field, params[field.name])}
                onChange={(value) => onUpdate(withActionParam(step, field.name, fieldValueToParam(field, value)))}
                disabled={isReadOnly}
                currentNodeType="action"
              />
            ))}
          </SectionFields>
        )}
        {missing.length > 0 && (
          <p className="cpv2-field-hint flex items-center gap-1.5 text-warning-ink">
            <AlertTriangle className="h-3.5 w-3.5 flex-shrink-0" aria-hidden />
            Required: {missing.join(", ")}
          </p>
        )}
      </Section>

      <ConnectIntegrationDialog
        target={connectOpen ? { ref: uses, integrationId: integration.id, displayName: integration.displayName, icon: integration.icon } : null}
        onClose={() => setConnectOpen(false)}
        onConnected={(connection) => onUpdate(withActionConnection(step, connection.id))}
      />
    </>
  );
}
