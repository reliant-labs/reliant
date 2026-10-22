// Copyright (c) 2025 Reliant Labs

/**
 * A workflow input whose type an integration defines (`github.repository`),
 * rendered as an IntegrationValuePicker with the same label/hint/error
 * chrome as every other input field. Every form that renders workflow inputs
 * dispatches dotted types here — the setup dialog, Run now, the composer's
 * params — so the picker comes with the type, not with the form.
 *
 * What the picker needs from around it travels in TypedInputsContext: the
 * connection the host chose per integration, who writes, how to connect, and
 * the labels of sibling inputs a scope names. A form with no provider still
 * works: the caller's default connection, no Connect button.
 */

import { createContext, useContext, useId, useMemo } from "react";

import type { InputDef } from "@/lib/inputHelpers";
import {
  getInputDescription,
  getInputMaxItems,
  getInputMinItems,
  getInputMulti,
  getIntegrationInputScope,
  integrationOfInputType,
} from "@/lib/inputHelpers";
import { isRequiredInput } from "../run/runWorkflowValues";
import { IntegrationValuePicker } from "./IntegrationValuePicker";
import { inputTypeNouns } from "./inputTypeNouns";

export interface TypedInputsContextValue {
  /** integration id → the connection the form chose; absent = the default. */
  connectionIds?: Record<string, string>;
  actingIdentity?: "owner" | "app";
  onConnect?: (integration: string) => void;
  /** integration id → display name ("GitHub"). */
  integrationNames?: Record<string, string>;
  /** input name → its label, for a scope's "under Repositories" hint. */
  inputLabels?: Record<string, string>;
  /** input name → its type, for a scope's plural ("repositories"). */
  inputTypes?: Record<string, string>;
  /** input name → error text, shown under the field. */
  errors?: Record<string, string>;
  /** input name → the DOM id its control gets, so an error summary can focus it. */
  fieldIds?: Record<string, string>;
}

const TypedInputsContext = createContext<TypedInputsContextValue>({});

export function TypedInputsProvider({ value, children }: { value: TypedInputsContextValue; children: React.ReactNode }) {
  const parent = useContext(TypedInputsContext);
  const merged = useMemo(
    () => ({
      ...parent,
      ...value,
      inputLabels: { ...parent.inputLabels, ...value.inputLabels },
      inputTypes: { ...parent.inputTypes, ...value.inputTypes },
    }),
    [parent, value],
  );
  return <TypedInputsContext.Provider value={merged}>{children}</TypedInputsContext.Provider>;
}

export function useTypedInputs(): TypedInputsContextValue {
  return useContext(TypedInputsContext);
}

function formatLabel(name: string): string {
  const base = name.includes(".") ? name.split(".").pop()! : name;
  return base.charAt(0).toUpperCase() + base.slice(1).replace(/_/g, " ");
}

/** Whether a scope input is empty, so the list it scopes cannot be asked for. */
function emptyScopeValue(value: unknown): boolean {
  return value === undefined || value === null || value === "" || (Array.isArray(value) && value.length === 0);
}

export interface TypedInputFieldProps {
  name: string;
  schema: InputDef;
  value: unknown;
  onChange: (value: unknown) => void;
  /** Every input's current value, flat, for resolving the scope. */
  values: Record<string, unknown>;
  disabled?: boolean;
  /** Override the label (the setup dialog words some inputs itself). */
  label?: string;
}

export function TypedInputField({ name, schema, value, onChange, values, disabled, label }: TypedInputFieldProps) {
  const ctx = useTypedInputs();
  const ids = useId();
  const type = schema.type ?? "";
  const integration = integrationOfInputType(type);
  const fieldId = ctx.fieldIds?.[name] ?? `${ids}-field`;
  const labelId = `${ids}-label`;
  const hintId = `${ids}-hint`;
  const errorId = `${ids}-error`;
  const shownLabel = label ?? ctx.inputLabels?.[name] ?? formatLabel(name);
  const description = getInputDescription(schema);
  const required = isRequiredInput(schema);
  const error = ctx.errors?.[name];

  // The scope binds the type's scope keys to sibling inputs; one empty
  // sibling is enough to make the list unaskable.
  const scopeBinding = getIntegrationInputScope(schema);
  const scopeInputs = Object.values(scopeBinding);
  const scope = useMemo(() => {
    const out: Record<string, unknown> = {};
    for (const [key, input] of Object.entries(scopeBinding)) out[key] = values[input];
    return out;
    // scopeBinding is derived from schema; values is the dependency that moves.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [schema, values]);
  const missingScope = scopeInputs.find((input) => emptyScopeValue(values[input]));
  const scopeType = missingScope ? ctx.inputTypes?.[missingScope] : undefined;

  return (
    <div className="space-y-1.5" data-field-key={name} data-testid={`typed-input-${name}`}>
      <div className="cpv2-field-label">
        <span id={labelId} className="flex items-center gap-1">
          {shownLabel}
          {required && (
            <>
              <span aria-hidden="true" className="text-destructive-ink">*</span>
              <span className="sr-only">(required)</span>
            </>
          )}
        </span>
      </div>
      <IntegrationValuePicker
        id={fieldId}
        labelId={labelId}
        label={shownLabel}
        describedBy={[description && hintId, error && errorId].filter(Boolean).join(" ") || undefined}
        type={type}
        multi={!!getInputMulti(schema)}
        minItems={getInputMinItems(schema)}
        maxItems={getInputMaxItems(schema)}
        required={required}
        value={value as string[] | string | undefined}
        onChange={onChange}
        connectionId={ctx.connectionIds?.[integration] || undefined}
        actingIdentity={ctx.actingIdentity}
        scope={scopeInputs.length > 0 ? scope : undefined}
        scopeMissing={!!missingScope}
        scopeLabel={missingScope ? (ctx.inputLabels?.[missingScope] ?? formatLabel(missingScope)) : undefined}
        scopePlural={scopeType ? inputTypeNouns(scopeType).plural : undefined}
        integrationName={ctx.integrationNames?.[integration]}
        onConnect={ctx.onConnect ? () => ctx.onConnect!(integration) : undefined}
        invalid={error}
        disabled={disabled}
      />
      {description && (
        <p id={hintId} className="cpv2-field-hint !mt-0">
          {description}
        </p>
      )}
      {error && (
        <p id={errorId} className="text-xs text-destructive-ink">
          {error}
        </p>
      )}
    </div>
  );
}
