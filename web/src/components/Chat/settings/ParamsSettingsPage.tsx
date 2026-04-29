// Copyright (c) 2025 Reliant Labs

import { useMemo, useState } from "react";
import { ChevronLeft, X, ChevronDown, ChevronRight, SlidersHorizontal } from "lucide-react";
import { WorkflowParamInput } from "../../workflow/WorkflowParamInput";
import { type InputDef, getInputUI, getInputDefault } from "../../../lib/inputHelpers";

const DEFAULT_EXCLUDE = ["model", "mode"];

interface ParamGroup {
  group: string;
  label: string;
  params: [string, InputDef][];
}

interface ParamsSettingsPageProps {
  inputs: Record<string, InputDef>;
  values: Record<string, unknown>;
  onChange: (values: Record<string, unknown>) => void;
  onBack: () => void;
  onClose: () => void;
  excludeParams?: string[];
}

export function ParamsSettingsPage({
  inputs,
  values,
  onChange,
  onBack,
  onClose,
  excludeParams = DEFAULT_EXCLUDE,
}: ParamsSettingsPageProps) {
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set([""]));

  const groups = useMemo(() => {
    const excluded = new Set(excludeParams);
    const configParams = Object.entries(inputs).filter(([name, schema]) => {
      if (excluded.has(name)) return false;
      if (getInputUI(schema) === "hidden") return false;
      if (schema.type === "message" || schema.type === "attachments" || schema.type === "tools" || schema.type === "preset") return false;
      return true;
    });

    const groupMap = new Map<string, [string, InputDef][]>();
    for (const [name, schema] of configParams) {
      const dotIndex = name.indexOf(".");
      const group = dotIndex > 0 ? name.substring(0, dotIndex) : "";
      if (!groupMap.has(group)) {
        groupMap.set(group, []);
      }
      groupMap.get(group)!.push([name, schema]);
    }

    return Array.from(groupMap.entries())
      .sort((a, b) => {
        if (a[0] === "") return -1;
        if (b[0] === "") return 1;
        return a[0].localeCompare(b[0]);
      })
      .map(([group, params]): ParamGroup => ({
        group,
        label: group || "Parameters",
        params,
      }));
  }, [inputs, excludeParams]);

  const toggleGroup = (group: string) => {
    setExpandedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(group)) {
        next.delete(group);
      } else {
        next.add(group);
      }
      return next;
    });
  };

  const handleParamChange = (name: string, value: unknown) => {
    onChange({ ...values, [name]: value });
  };

  const getParamValue = (name: string, schema: InputDef): unknown => {
    return values[name] ?? getInputDefault(schema);
  };

  if (groups.length === 0) {
    return (
      <div>
        <div className="flex items-center gap-2 border-b border-border/50 px-3 py-2.5">
          <button onClick={onBack} className="rounded-lg p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground">
            <ChevronLeft className="h-4 w-4" />
          </button>
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <div className="flex h-6 w-6 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <SlidersHorizontal className="h-3.5 w-3.5" />
            </div>
            <h3 className="text-sm font-semibold">Settings</h3>
          </div>
          <button onClick={onClose} className="rounded-lg p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground">
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
        <div className="px-4 py-8 text-center text-sm text-muted-foreground">
          No additional settings available
        </div>
      </div>
    );
  }

  return (
    <div>
      {/* Header */}
      <div className="flex items-center gap-2 border-b border-border/50 px-3 py-2.5">
        <button onClick={onBack} className="rounded-lg p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground">
          <ChevronLeft className="h-4 w-4" />
        </button>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <div className="flex h-6 w-6 items-center justify-center rounded-lg bg-primary/10 text-primary">
            <SlidersHorizontal className="h-3.5 w-3.5" />
          </div>
          <div className="min-w-0">
            <h3 className="text-sm font-semibold leading-none">Settings</h3>
            <p className="mt-1 text-[11px] text-muted-foreground">
              Tune workflow parameters for this chat
            </p>
          </div>
        </div>
        <button onClick={onClose} className="rounded-lg p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground">
          <X className="h-3.5 w-3.5" />
        </button>
      </div>

      {/* Scrollable content */}
      <div className="max-h-[360px] space-y-3 overflow-y-auto bg-muted/10 px-3 py-3">
        {groups.map((paramGroup) => {
          const isExpanded = !paramGroup.group || expandedGroups.has(paramGroup.group);
          return (
            <section
              key={paramGroup.group}
              className="overflow-visible rounded-2xl border border-border/40 bg-background/70 p-2.5 shadow-sm shadow-black/5"
            >
              {/* Named group header (collapsible) */}
              {paramGroup.group ? (
                <button
                  type="button"
                  onClick={() => toggleGroup(paramGroup.group)}
                  className="mb-2 flex w-full items-center gap-2 rounded-xl px-1.5 py-1 text-left text-xs font-semibold uppercase tracking-wide text-muted-foreground transition-colors hover:bg-accent/40 hover:text-foreground"
                >
                  {isExpanded ? (
                    <ChevronDown className="h-3.5 w-3.5" />
                  ) : (
                    <ChevronRight className="h-3.5 w-3.5" />
                  )}
                  <span className="flex-1 truncate">{paramGroup.label}</span>
                  <span className="rounded-full bg-primary/10 px-1.5 py-0.5 text-[10px] font-semibold text-primary">
                    {paramGroup.params.length}
                  </span>
                </button>
              ) : (
                <div className="mb-2 flex items-center justify-between px-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  <span>{paramGroup.label}</span>
                  <span className="rounded-full bg-primary/10 px-1.5 py-0.5 text-[10px] text-primary">
                    {paramGroup.params.length}
                  </span>
                </div>
              )}

              {/* Params */}
              {isExpanded && (
                <div className="space-y-2.5">
                  {paramGroup.params.map(([name, schema]) => (
                    <WorkflowParamInput
                      key={name}
                      name={name}
                      schema={schema}
                      value={getParamValue(name, schema)}
                      onChange={(val) => handleParamChange(name, val)}
                      hideCELToggle
                      formValues={values}
                      isChatInputContext
                    />
                  ))}
                </div>
              )}
            </section>
          );
        })}
      </div>
    </div>
  );
}

export default ParamsSettingsPage;