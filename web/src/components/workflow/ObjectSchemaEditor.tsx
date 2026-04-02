// Copyright (c) 2025 Reliant Labs

import { useState, useRef, useEffect } from "react";
import { Plus, X, ChevronDown, ChevronRight, Check } from "lucide-react";
import { cn } from "../../lib/utils";
import type { InputDef } from "../../lib/inputHelpers";
import { getInputProperties, getInputRequired } from "../../lib/inputHelpers";
import type { PropertySchema } from "./WorkflowParamInput";

interface ObjectSchemaEditorProps {
  schema: InputDef;
  value: unknown;
  onChange: (value: unknown) => void;
  disabled?: boolean;
  depth?: number;
}

export function ObjectSchemaEditor({
  schema,
  value,
  onChange,
  disabled = false,
  depth = 0,
}: ObjectSchemaEditorProps) {
  const properties = getInputProperties(schema) || {};
  const required = getInputRequired(schema) || [];
  const currentValue = (value as Record<string, unknown>) || {};

  // Add new property
  const addProperty = (name: string) => {
    if (!name || properties[name]) return;

    onChange({
      ...currentValue,
      [name]: getDefaultValueForType("string"),
    });

    // Note: Schema property updates are handled by WorkflowParamsEditor
  };

  // Remove property
  const removeProperty = (name: string) => {
    const newProperties = { ...properties };
    delete newProperties[name];

    const newValue = { ...currentValue };
    delete newValue[name];

    onChange(newValue);
  };

  // Update property value
  const updatePropertyValue = (name: string, propValue: unknown) => {
    onChange({
      ...currentValue,
      [name]: propValue,
    });
  };

  const propertyNames = Object.keys(properties);

  return (
    <div className={cn("space-y-3", depth > 0 && "pl-4 border-l-2 border-border/50")}>
      {propertyNames.length === 0 ? (
        <div className="text-sm text-muted-foreground py-4 text-center border-2 border-dashed border-border rounded-lg">
          No properties defined. Add properties to define the object structure.
        </div>
      ) : (
        <div className="space-y-2">
          {propertyNames.map((name) => {
            const propSchema = properties[name];
            const isRequired = required.includes(name);

            return (
              <PropertyEditor
                key={name}
                name={name}
                schema={propSchema}
                value={currentValue[name]}
                onChange={(val) => updatePropertyValue(name, val)}
                onRemove={() => removeProperty(name)}
                isRequired={isRequired}
                disabled={disabled}
                depth={depth}
              />
            );
          })}
        </div>
      )}

      {!disabled && (
        <AddPropertyButton onAdd={addProperty} />
      )}
    </div>
  );
}

interface PropertyEditorProps {
  name: string;
  schema: PropertySchema;
  value: unknown;
  onChange: (value: unknown) => void;
  onRemove: () => void;
  isRequired: boolean;
  disabled: boolean;
  depth: number;
}

function PropertyEditor({
  name,
  schema,
  value,
  onChange,
  onRemove,
  isRequired,
  disabled,
  depth,
}: PropertyEditorProps) {
  const [isExpanded, setIsExpanded] = useState(depth === 0);

  const renderPropertyInput = () => {
    switch (schema.type) {
      case "string":
        return (
          <StringPropertyInput
            schema={schema}
            value={value as string}
            onChange={onChange}
            disabled={disabled}
          />
        );

      case "number":
      case "integer":
        return (
          <NumberPropertyInput
            schema={schema}
            value={value as number}
            onChange={onChange}
            disabled={disabled}
          />
        );

      case "boolean":
        return (
          <BooleanPropertyInput
            value={value as boolean}
            onChange={onChange}
            disabled={disabled}
          />
        );

      case "array":
        return (
          <div className="text-xs text-muted-foreground">
            Array type - define items schema in constraints
          </div>
        );

      case "object":
        if (schema.properties) {
          return (
            <ObjectSchemaEditor
              schema={{
                type: "object",
                properties: schema.properties,
                required: schema.required,
              } as InputDef}
              value={value}
              onChange={onChange}
              disabled={disabled}
              depth={depth + 1}
            />
          );
        }
        return (
          <div className="text-xs text-muted-foreground">
            Object type - define properties in constraints
          </div>
        );

      default:
        return null;
    }
  };

  const hasNestedContent = schema.type === "object" && schema.properties;

  return (
    <div className="border border-border rounded-lg overflow-hidden bg-background">
      <div className="p-3 space-y-3">
        {/* Property header */}
        <div className="flex items-start gap-2">
          {hasNestedContent && (
            <button
              type="button"
              onClick={() => setIsExpanded(!isExpanded)}
              className="p-1 hover:bg-accent rounded transition-colors"
            >
              {isExpanded ? (
                <ChevronDown className="w-4 h-4" />
              ) : (
                <ChevronRight className="w-4 h-4" />
              )}
            </button>
          )}

          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2">
              <span className="font-medium text-sm font-mono">{name}</span>
              {isRequired && (
                <span className="text-xs px-1.5 py-0.5 rounded bg-destructive/10 text-destructive font-medium">
                  Required
                </span>
              )}
              <span className="text-xs px-1.5 py-0.5 rounded bg-muted text-muted-foreground">
                {schema.type}
              </span>
            </div>

            {schema.description && (
              <p className="text-xs text-muted-foreground mt-1">
                {schema.description}
              </p>
            )}
          </div>

          {!disabled && (
            <button
              type="button"
              onClick={onRemove}
              className="p-1 hover:bg-destructive/10 hover:text-destructive rounded transition-colors"
              title="Remove property"
            >
              <X className="w-4 h-4" />
            </button>
          )}
        </div>

        {/* Property input (only show if not nested object or if expanded) */}
        {(!hasNestedContent || isExpanded) && (
          <div className="mt-2">
            {renderPropertyInput()}
          </div>
        )}

        {/* Constraints display */}
        {(schema.enum || schema.minimum !== undefined || schema.maximum !== undefined || 
          schema.minLength !== undefined || schema.maxLength !== undefined) && (
          <div className="text-xs text-muted-foreground space-y-1 mt-2 pt-2 border-t border-border/50">
            {schema.enum && (
              <div>Allowed values: {schema.enum.join(", ")}</div>
            )}
            {schema.minimum !== undefined && (
              <div>Minimum: {schema.minimum}</div>
            )}
            {schema.maximum !== undefined && (
              <div>Maximum: {schema.maximum}</div>
            )}
            {schema.minLength !== undefined && (
              <div>Min length: {schema.minLength}</div>
            )}
            {schema.maxLength !== undefined && (
              <div>Max length: {schema.maxLength}</div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// String property input
function StringPropertyInput({
  schema,
  value,
  onChange,
  disabled,
}: {
  schema: PropertySchema;
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const currentValue = value ?? "";

  if (schema.enum && schema.enum.length > 0) {
    return (
      <EnumSelect
        options={schema.enum.map(v => String(v))}
        value={currentValue}
        onChange={onChange}
        disabled={disabled}
      />
    );
  }

  return (
    <input
      type="text"
      value={currentValue}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      className={cn(
        "w-full px-3 py-1.5 text-sm rounded-md border border-border bg-background",
        "focus:outline-none focus:ring-2 focus:ring-ring",
        disabled && "opacity-50 cursor-not-allowed"
      )}
      placeholder="Enter value..."
    />
  );
}

// Number property input
function NumberPropertyInput({
  schema,
  value,
  onChange,
  disabled,
}: {
  schema: PropertySchema;
  value: number;
  onChange: (value: number) => void;
  disabled: boolean;
}) {
  const currentValue = value ?? schema.minimum ?? 0;

  return (
    <input
      type="number"
      value={currentValue}
      onChange={(e) => {
        const val = parseFloat(e.target.value);
        if (!isNaN(val)) {
          onChange(val);
        }
      }}
      min={schema.minimum}
      max={schema.maximum}
      step={schema.type === "integer" ? 1 : 0.1}
      disabled={disabled}
      className={cn(
        "w-full px-3 py-1.5 text-sm rounded-md border border-border bg-background",
        "focus:outline-none focus:ring-2 focus:ring-ring",
        disabled && "opacity-50 cursor-not-allowed"
      )}
    />
  );
}

// Boolean property input
function BooleanPropertyInput({
  value,
  onChange,
  disabled,
}: {
  value: boolean;
  onChange: (value: boolean) => void;
  disabled: boolean;
}) {
  const isOn = value ?? false;

  return (
    <button
      type="button"
      onClick={() => !disabled && onChange(!isOn)}
      disabled={disabled}
      className={cn(
        "relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
        isOn ? "bg-primary" : "bg-muted",
        disabled && "opacity-50 cursor-not-allowed"
      )}
    >
      <span
        className={cn(
          "pointer-events-none inline-block h-5 w-5 transform rounded-full bg-background shadow-lg ring-0 transition-transform",
          isOn ? "translate-x-5" : "translate-x-0"
        )}
      />
    </button>
  );
}

// Enum select dropdown
function EnumSelect({
  options,
  value,
  onChange,
  disabled,
}: {
  options: string[];
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
}) {
  const [isOpen, setIsOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };

    if (isOpen) {
      document.addEventListener("mousedown", handleClickOutside);
      return () => document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [isOpen]);

  return (
    <div ref={dropdownRef} className="relative">
      <button
        type="button"
        onClick={() => !disabled && setIsOpen(!isOpen)}
        disabled={disabled}
        className={cn(
          "flex items-center justify-between gap-2 w-full px-3 py-1.5 text-sm rounded-md",
          "border border-border bg-background",
          "focus:outline-none focus:ring-2 focus:ring-ring",
          disabled && "opacity-50 cursor-not-allowed"
        )}
      >
        <span className="truncate">{value || "Select..."}</span>
        <ChevronDown className="w-4 h-4 opacity-50" />
      </button>

      {isOpen && (
        <div className="absolute top-full left-0 right-0 mt-1 z-50 rounded-md border border-border bg-popover shadow-lg">
          <div className="py-1 max-h-48 overflow-y-auto">
            {options.map((option) => (
              <button
                key={option}
                onClick={() => {
                  onChange(option);
                  setIsOpen(false);
                }}
                className={cn(
                  "w-full px-3 py-1.5 text-left text-sm transition-colors",
                  value === option ? "bg-accent" : "hover:bg-accent/50"
                )}
              >
                <div className="flex items-center justify-between">
                  <span>{option}</span>
                  {value === option && <Check className="w-4 h-4 text-primary" />}
                </div>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// Add property button
function AddPropertyButton({ onAdd }: { onAdd: (name: string) => void }) {
  const [isAdding, setIsAdding] = useState(false);
  const [newName, setNewName] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (isAdding) {
      inputRef.current?.focus();
    }
  }, [isAdding]);

  const handleAdd = () => {
    if (newName.trim()) {
      onAdd(newName.trim());
      setNewName("");
      setIsAdding(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      handleAdd();
    } else if (e.key === "Escape") {
      setNewName("");
      setIsAdding(false);
    }
  };

  if (isAdding) {
    return (
      <div className="flex items-center gap-2">
        <input
          ref={inputRef}
          type="text"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onKeyDown={handleKeyDown}
          onBlur={() => {
            if (!newName.trim()) {
              setIsAdding(false);
            }
          }}
          placeholder="property_name"
          className={cn(
            "flex-1 px-3 py-1.5 text-sm rounded-md border border-border bg-background",
            "focus:outline-none focus:ring-2 focus:ring-ring font-mono"
          )}
        />
        <button
          type="button"
          onClick={handleAdd}
          className="px-3 py-1.5 text-sm rounded-md bg-primary text-primary-foreground hover:bg-primary/90 transition-colors"
        >
          Add
        </button>
        <button
          type="button"
          onClick={() => {
            setNewName("");
            setIsAdding(false);
          }}
          className="px-3 py-1.5 text-sm rounded-md border border-border hover:bg-accent transition-colors"
        >
          Cancel
        </button>
      </div>
    );
  }

  return (
    <button
      type="button"
      onClick={() => setIsAdding(true)}
      className={cn(
        "w-full flex items-center justify-center gap-2 px-3 py-2 text-sm rounded-md",
        "border-2 border-dashed border-border hover:border-primary/50 hover:bg-accent/50",
        "text-muted-foreground hover:text-foreground transition-colors"
      )}
    >
      <Plus className="w-4 h-4" />
      Add Property
    </button>
  );
}

// Helper to get default value for a type
function getDefaultValueForType(type: string): unknown {
  switch (type) {
    case "string":
      return "";
    case "number":
    case "integer":
      return 0;
    case "boolean":
      return false;
    case "array":
      return [];
    case "object":
      return {};
    default:
      return null;
  }
}