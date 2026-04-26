// Copyright (c) 2025 Reliant Labs

import { useMemo } from "react";
import { ChevronLeft, X, ChevronDown, ChevronRight } from "lucide-react";
import { cn } from "../../../lib/utils";
import { WorkflowParamInput } from "../../workflow/WorkflowParamInput";
import { type InputDef, getInputUI, getInputDefault, getInputDescription } from "../../../lib/inputHelpers";
import { useState } from "react";

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
        <div className="flex items-center gap-2 px-3 py-2.5 border-b border-border">
          <button onClick={onBack} className="p-1 rounded hover:bg-accent transition-colors">
            <ChevronLeft className="w-4 h-4 text-muted-foreground" />
          </button>
          <h3 className="text-sm font-semibold flex-1">Settings</h3>
          <button onClick={onClose} className="p-1 rounded hover:bg-accent transition-colors">
            <X className="w-3.5 h-3.5 text-muted-foreground" />
          </button>
        </div>
        <div className="px-4 py-6 text-center text-sm text-muted-foreground">
          No additional settings available
        </div>
      </div>
    );
  }

  return (
    <div>
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-2.5 border-b border-border">
        <button onClick={onBack} className="p-1 rounded hover:bg-accent transition-colors">
          <ChevronLeft className="w-4 h-4 text-muted-foreground" />
        </button>
        <h3 className="text-sm font-semibold flex-1">Settings</h3>
        <button onClick={onClose} className="p-1 rounded hover:bg-accent transition-colors">
          <X className="w-3.5 h-3.5 text-muted-foreground" />
        </button>
      </div>

      {/* Scrollable content */}
      <div className="overflow-y-auto max-h-[350px] px-4 py-3">
        {groups.map((paramGroup) => (
          <div
            key={paramGroup.group}
            className="border-t border-border/50 first:border-t-0 pt-2 first:pt-0"
          >
            {/* Named group header (collapsible) */}
            {paramGroup.group ? (
              <button
                type="button"
                onClick={() => toggleGroup(paramGroup.group)}
                className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground hover:text-foreground transition-colors mb-2 w-full"
              >
                {expandedGroups.has(paramGroup.group) ? (
                  <ChevronDown className="w-3.5 h-3.5" />
                ) : (
                  <ChevronRight className="w-3.5 h-3.5" />
                )}
                {paramGroup.label}
                <span className="text-[10px] font-normal">({paramGroup.params.length})</span>
              </button>
            ) : null}

            {/* Params */}
            {(!paramGroup.group || expandedGroups.has(paramGroup.group)) && (
              <div className="space-y-3">
                {paramGroup.params.map(([name, schema]) => (
                  <div key={name}>
                    <WorkflowParamInput
                      name={name}
                      schema={schema}
                      value={getParamValue(name, schema)}
                      onChange={(val) => handleParamChange(name, val)}
                      hideCELToggle
                      formValues={values}
                      isChatInputContext
                    />
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

export default ParamsSettingsPage;