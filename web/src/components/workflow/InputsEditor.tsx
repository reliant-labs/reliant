import { useState, useCallback } from 'react';
import { Plus, Trash2, ChevronDown, ChevronRight } from 'lucide-react';
import { CELInput } from './CELInput';
import { formatValueForDisplay } from '../../lib/paramUtils';

interface InputsEditorProps {
  inputs: Record<string, any> | undefined;
  onChange: (inputs: Record<string, any>) => void;
  /** Known input fields with metadata (optional) */
  knownFields?: InputFieldDef[];
  /** Keys to exclude from the editor (handled elsewhere) */
  excludeKeys?: string[];
  /** Whether to show the section collapsed by default */
  defaultCollapsed?: boolean;
  /** Title for the section */
  title?: string;
  /** Node IDs available for CEL completions */
  nodeIds?: string[];
  /** Input params available for CEL completions */
  inputParams?: Record<string, { type: string; description?: string }>;
}

export interface InputFieldDef {
  name: string;
  type: 'string' | 'number' | 'boolean' | 'cel';
  description?: string;
  placeholder?: string;
  required?: boolean;
}

/**
 * Generic key-value input editor for step inputs.
 * Supports:
 * - Known fields with specific types
 * - Dynamic key-value pairs
 * - CEL expressions with autocomplete
 */
export function InputsEditor({
  inputs,
  onChange,
  knownFields = [],
  excludeKeys = [],
  defaultCollapsed = true,
  title = 'Additional Inputs',
  nodeIds,
  inputParams,
}: InputsEditorProps) {
  const [isExpanded, setIsExpanded] = useState(!defaultCollapsed);
  
  // Get keys that are handled by this editor
  const knownKeys = new Set(knownFields.map(f => f.name));
  const excludeSet = new Set(excludeKeys);
  
  // Custom entries: not in knownFields AND not excluded
  const customEntries = Object.entries(inputs || {}).filter(
    ([key]) => !knownKeys.has(key) && !excludeSet.has(key)
  );
  
  // Count inputs that this editor manages (known fields + custom)
  const knownWithValues = knownFields.filter(f => inputs?.[f.name] !== undefined);
  const managedInputCount = knownWithValues.length + customEntries.length;

  const updateInput = useCallback((key: string, value: any) => {
    const newInputs = { ...inputs };
    if (value === undefined || value === '') {
      delete newInputs[key];
    } else {
      newInputs[key] = value;
    }
    onChange(newInputs);
  }, [inputs, onChange]);

  const addCustomInput = useCallback(() => {
    // Generate unique key
    const existingKeys = new Set(Object.keys(inputs || {}));
    let counter = 1;
    while (existingKeys.has(`input${counter}`)) {
      counter++;
    }
    onChange({ ...inputs, [`input${counter}`]: '' });
  }, [inputs, onChange]);

  const renameKey = useCallback((oldKey: string, newKey: string) => {
    if (oldKey === newKey || !newKey.trim()) return;
    const sanitizedKey = newKey.trim().replace(/\s+/g, '_');
    if (inputs?.[sanitizedKey] !== undefined && sanitizedKey !== oldKey) return;
    
    const newInputs: Record<string, any> = {};
    for (const [key, value] of Object.entries(inputs || {})) {
      if (key === oldKey) {
        newInputs[sanitizedKey] = value;
      } else {
        newInputs[key] = value;
      }
    }
    onChange(newInputs);
  }, [inputs, onChange]);

  const removeInput = useCallback((key: string) => {
    const newInputs = { ...inputs };
    delete newInputs[key];
    onChange(newInputs);
  }, [inputs, onChange]);

  const renderInput = (key: string, value: any, field?: InputFieldDef) => {
    const isBoolean = field?.type === 'boolean' || typeof value === 'boolean';
    const isNumber = field?.type === 'number' || typeof value === 'number';
    const isCEL = field?.type === 'cel' || (typeof value === 'string' && value.includes('{{'));

    if (isBoolean) {
      return (
        <select
          value={value ? 'true' : 'false'}
          onChange={(e) => updateInput(key, e.target.value === 'true')}
          className="flex-1 px-2 py-1.5 border border-input rounded text-sm bg-background text-foreground"
        >
          <option value="true">true</option>
          <option value="false">false</option>
        </select>
      );
    }

    if (isNumber && !isCEL) {
      return (
        <input
          type="number"
          value={value ?? ''}
          onChange={(e) => updateInput(key, e.target.value ? Number(e.target.value) : undefined)}
          placeholder={field?.placeholder}
          className="flex-1 px-2 py-1.5 border border-input rounded text-sm bg-background text-foreground"
        />
      );
    }

    // Default to CEL-enabled text input
    return (
      <CELInput
        value={formatValueForDisplay(value ?? '')}
        onChange={(val) => updateInput(key, val || undefined)}
        placeholder={field?.placeholder || 'Value or {{CEL expression}}'}
        nodeIds={nodeIds}
        inputParams={inputParams}
        className="flex-1"
        hideCELHint
      />
    );
  };

  return (
    <div className="border-t border-border pt-4">
      <button
        onClick={() => setIsExpanded(!isExpanded)}
        className="flex items-center gap-2 text-sm font-medium text-foreground hover:text-foreground w-full"
      >
        {isExpanded ? (
          <ChevronDown className="w-4 h-4" />
        ) : (
          <ChevronRight className="w-4 h-4" />
        )}
        {title}
        {managedInputCount > 0 && (
          <span className="ml-auto text-xs text-blue-600">
            {managedInputCount} configured
          </span>
        )}
      </button>

      {isExpanded && (
        <div className="mt-3 space-y-3">
          {/* Known fields */}
          {knownFields.map((field) => (
            <div key={field.name}>
              <label className="block text-xs font-medium text-foreground mb-1">
                {field.name}
                {field.required && <span className="text-destructive ml-1">*</span>}
              </label>
              {renderInput(field.name, inputs?.[field.name], field)}
              {field.description && (
                <p className="mt-1 text-xs text-muted-foreground">{field.description}</p>
              )}
            </div>
          ))}

          {/* Custom key-value pairs */}
          {customEntries.map(([key, value]) => (
            <div key={key} className="flex items-start gap-2">
              <input
                type="text"
                value={key}
                onChange={(e) => renameKey(key, e.target.value)}
                className="w-28 px-2 py-1.5 border border-input rounded text-sm bg-background text-foreground"
                placeholder="key"
              />
              {renderInput(key, value)}
              <button
                onClick={() => removeInput(key)}
                className="p-1.5 text-muted-foreground hover:text-destructive transition-colors"
                title="Remove"
              >
                <Trash2 className="w-4 h-4" />
              </button>
            </div>
          ))}

          {/* Add button */}
          <button
            onClick={addCustomInput}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground border border-dashed border-border rounded hover:border-primary transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
            Add input
          </button>
        </div>
      )}
    </div>
  );
}