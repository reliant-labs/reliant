import { Code } from 'lucide-react';
import { HelpPopover } from '../ui/HelpPopover';
import { MonacoCELEditor } from './MonacoCELEditor';
import { useCELCompletionContext } from './CELCompletionContext';

export interface CELInputProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  /** Single line input vs multi-line textarea */
  multiline?: boolean;
  /** Number of rows for textarea (default: 2) */
  rows?: number;
  /** Label shown above input */
  label?: string;
  /** Help text shown below input (always visible) */
  helpText?: string;
  /** Help tooltip shown via ? icon (cleaner UX) */
  helpTooltip?: string;
  /** Additional CSS classes */
  className?: string;
  /** Disable the input */
  disabled?: boolean;
  /** Show CEL mode indicator */
  showCELIndicator?: boolean;
  /** Whether this field ONLY accepts CEL (no {{ }} wrapper) */
  pureExpression?: boolean;
  /** Hide the "Use {{ }} for dynamic values" hint (default: false) */
  hideCELHint?: boolean;

  // Completion context (optional)
  nodeIds?: string[];
  nodeTypeMap?: Record<string, string>;
  inputParams?: Record<string, { type: string; description?: string }>;
  celContext?: 'default' | 'loop_while' | 'edge_condition' | 'save_message' | 'thread';
  currentNodeType?: string;
}

/**
 * CELInput - A Monaco-powered input for CEL expressions
 * 
 * Two modes:
 * - Templated (default): Values are strings, use {{ expr }} for CEL interpolation
 * - Pure expression (pureExpression=true): Entire value is evaluated as CEL
 */
export function CELInput({
  value,
  onChange,
  placeholder = 'Enter value or CEL expression...',
  multiline = false,
  rows = 2,
  label,
  helpText,
  helpTooltip,
  className,
  disabled = false,
  showCELIndicator = true,
  pureExpression = false,
  hideCELHint = false,
  nodeIds,
  nodeTypeMap,
  inputParams,
  celContext,
  currentNodeType,
}: CELInputProps) {
  const hasTemplate = value.includes('{{');
  const isCELMode = pureExpression || hasTemplate;

  // Fall back to React context when explicit props aren't provided
  const ctx = useCELCompletionContext();

  const resolvedNodeIds = nodeIds ?? ctx?.nodeIds;
  const resolvedNodeTypeMap = nodeTypeMap ?? ctx?.nodeTypeMap;
  const resolvedInputParams = inputParams ?? ctx?.inputParams;
  const resolvedNodeDeclaredOutputs = ctx?.nodeDeclaredOutputs;

  return (
    <div className="relative">
      {label && (
        <label className="block text-sm font-medium text-foreground mb-1 flex items-center gap-2">
          {label}
          {showCELIndicator && (pureExpression || isCELMode) && (
            <span className="inline-flex items-center gap-1 text-[10px] text-muted-foreground bg-muted/80 px-1.5 py-0.5 rounded-sm font-mono">
              <Code className="w-3 h-3" />
              CEL
            </span>
          )}
          {helpTooltip && (
            <HelpPopover content={helpTooltip} title={label} />
          )}
        </label>
      )}

      <MonacoCELEditor
        value={value}
        onChange={onChange}
        placeholder={placeholder}
        multiline={multiline}
        rows={rows}
        disabled={disabled}
        pureExpression={pureExpression}
        nodeIds={resolvedNodeIds}
        nodeTypeMap={resolvedNodeTypeMap}
        inputParams={resolvedInputParams}
        celContext={celContext}
        currentNodeType={currentNodeType}
        nodeDeclaredOutputs={resolvedNodeDeclaredOutputs}
        className={className}
      />

      {helpText && (
        <p className="mt-1 text-xs text-muted-foreground">
          {helpText}
        </p>
      )}

      {/* Help text for templated mode */}
      {!hideCELHint && !pureExpression && !isCELMode && !disabled && (
        <div className="mt-1.5 text-xs text-muted-foreground">
          Use <code className="bg-muted px-1 rounded font-mono">{'{{'}</code> <code className="bg-muted px-1 rounded font-mono">{'}}'}</code> for dynamic values, e.g. <code className="bg-muted px-1 rounded font-mono">{'{{nodes.llm.response_text}}'}</code>
        </div>
      )}

      {/* Help text for pure CEL mode */}
      {!hideCELHint && pureExpression && !disabled && (
        <div className="mt-1.5 text-xs text-muted-foreground">
          CEL expression evaluated directly, e.g. <code className="bg-muted px-1 rounded font-mono">nodes.agent.tool_calls.size() == 0</code>
        </div>
      )}
    </div>
  );
}

/**
 * CEL expression input for condition fields (no {{ }} wrapper needed)
 * Used for: switch conditions, loop while, edge conditions
 */
export function CELExpressionInput(props: Omit<CELInputProps, 'pureExpression'>) {
  return <CELInput {...props} pureExpression={true} />;
}