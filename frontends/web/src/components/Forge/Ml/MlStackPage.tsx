// Copyright (c) 2026 Reliant Labs

/**
 * ML › Stack (MlEarlyAccessService.GetMlStack): the ML components discovered
 * in each connected cluster — adopted (found, theirs), installed by forge
 * (not found, so installed from a pinned chart), or missing — each with its
 * version, how it got there, and the KCL `forge ml discover` is planned to
 * propose for it (labelled planned: forge has no ML declarations yet).
 */

import { useMemo } from "react";

import Status from "@/components/forge-ui/status";
import { useMlStack } from "@/hooks/ml-queries";
import type { MlStackComponent } from "@/services/controlPlane/ml/types";

import { COMPONENT_STATE } from "./mlModel";
import { CodeBlock, MlFrame, MlRead, Section, useMlGate } from "./MlParts";

/** A declaration's caption: the KCL is the proposal's, not a released forge API. */
export const PLANNED_KCL_LABEL = "Planned: the KCL forge ml discover will propose (not released yet)";

export function MlStackPage() {
  const { orgId } = useMlGate();
  const query = useMlStack(orgId);
  return (
    <MlFrame view="stack">
      <MlRead query={query} what="ML stack">
        {(components) => <StackView components={components} />}
      </MlRead>
    </MlFrame>
  );
}

function StackView({ components }: { components: MlStackComponent[] }) {
  const byCluster = useMemo(() => {
    const groups = new Map<string, MlStackComponent[]>();
    for (const component of components) groups.set(component.clusterId, [...(groups.get(component.clusterId) ?? []), component]);
    return [...groups.entries()];
  }, [components]);
  if (components.length === 0) {
    return <p className="text-ui text-ink-muted" data-testid="ml-stack-none">Discovery found no ML components in the connected clusters.</p>;
  }
  return (
    <div className="space-y-4" data-testid="ml-stack">
      {byCluster.map(([clusterId, list]) => {
        const missing = list.filter((c) => c.state === "missing").length;
        return (
          <Section key={clusterId} title={clusterId} note={`${list.length} components${missing ? ` · ${missing} missing` : ""}`} testId={`ml-stack-${clusterId}`} flush>
            <ul className="divide-y divide-border-subtle border-t border-border-subtle">
              {list.map((component) => (
                <StackRow key={`${component.clusterId}/${component.name}`} component={component} />
              ))}
            </ul>
          </Section>
        );
      })}
    </div>
  );
}

function StackRow({ component }: { component: MlStackComponent }) {
  const state = COMPONENT_STATE[component.state];
  return (
    <li className="space-y-2 px-4 py-3" data-testid={`ml-component-${component.name}`} data-state={component.state}>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span className="font-mono text-code font-medium text-ink">{component.name}</span>
        <span title={state.note || undefined}>
          <Status tone={state.tone} label={state.label} />
        </span>
        {component.version && <span className="font-mono text-code text-ink-muted">{component.version}</span>}
        {component.category && <span className="text-meta text-ink-muted">{component.category}</span>}
      </div>
      {(component.via || component.namespace || component.note) && (
        <p className="text-ui text-ink-muted">
          {[component.via && `via ${component.via}`, component.namespace && `in ${component.namespace}`].filter(Boolean).join(" · ")}
          {component.note && <span className="block">{component.note}</span>}
        </p>
      )}
      {component.declaration && <CodeBlock code={component.declaration} label={PLANNED_KCL_LABEL} testId={`ml-component-kcl-${component.name}`} />}
    </li>
  );
}
