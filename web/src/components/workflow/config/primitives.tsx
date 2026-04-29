/**
 * Config panel UI primitives — React wrappers for the cpv2-* CSS classes.
 * Matches the design sandbox: workflow-config-panel-redesign.html
 */
import { useState, type ReactNode, type InputHTMLAttributes, type TextareaHTMLAttributes, type SelectHTMLAttributes } from "react";
import { ChevronDown, ChevronRight, Plus } from "lucide-react";

// ---------------------------------------------------------------------------
// Section
// ---------------------------------------------------------------------------

export function Section({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <div className={`cpv2-section ${className}`}>{children}</div>;
}

export function SectionLabel({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <div className={`cpv2-section-label ${className}`}>{children}</div>;
}

export function SectionFields({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <div className={`cpv2-section-fields ${className}`}>{children}</div>;
}

// ---------------------------------------------------------------------------
// Field layout
// ---------------------------------------------------------------------------

/** Horizontal row: label on left, control on right */
export function FieldInline({
  label,
  hint,
  children,
  className = "",
}: {
  label: ReactNode;
  hint?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={`cpv2-field-inline ${className}`}>
      <span className="cpv2-fi-label">
        {label}
        {hint && <span className="cpv2-fi-hint"> · {hint}</span>}
      </span>
      {children}
    </div>
  );
}

/** Stacked field label with optional right-side action (e.g. CEL toggle) */
export function FieldLabel({ children, action, className = "" }: { children: ReactNode; action?: ReactNode; className?: string }) {
  return (
    <div className={`cpv2-field-label ${className}`}>
      <span>{children}</span>
      {action}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Inputs
// ---------------------------------------------------------------------------

export function FieldInput(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={`cpv2-field-input ${props.className ?? ""}`} />;
}

export function FieldTextarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={`cpv2-field-textarea ${props.className ?? ""}`} />;
}

export function FieldSelect({ children, ...props }: SelectHTMLAttributes<HTMLSelectElement> & { children: ReactNode }) {
  return <select {...props} className={`cpv2-field-select ${props.className ?? ""}`}>{children}</select>;
}

export function FieldSelectInline({ children, ...props }: SelectHTMLAttributes<HTMLSelectElement> & { children: ReactNode }) {
  return <select {...props} className={`cpv2-field-select-inline ${props.className ?? ""}`}>{children}</select>;
}

export function FieldNumber(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input type="number" {...props} className={`cpv2-field-number ${props.className ?? ""}`} />;
}

// ---------------------------------------------------------------------------
// CEL
// ---------------------------------------------------------------------------

export function CelToggle({ active, onClick }: { active?: boolean; onClick?: () => void }) {
  return (
    <button type="button" className={`cpv2-cel-toggle${active ? " active" : ""}`} onClick={onClick}>
      CEL
    </button>
  );
}

export function CelField(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={`cpv2-cel-field ${props.className ?? ""}`} />;
}

// ---------------------------------------------------------------------------
// Slider
// ---------------------------------------------------------------------------

export function SliderRow({
  value,
  min = 0,
  max = 100,
  step = 1,
  displayValue,
  onChange,
}: {
  value: number;
  min?: number;
  max?: number;
  step?: number;
  displayValue?: string;
  onChange: (value: number) => void;
}) {
  return (
    <div className="cpv2-slider-row">
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
      />
      <span className="cpv2-slider-val">{displayValue ?? value}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tags
// ---------------------------------------------------------------------------

export function TagRow({ children }: { children: ReactNode }) {
  return <div className="cpv2-tag-row">{children}</div>;
}

export function Tag({
  children,
  selected,
  dashed,
  onRemove,
  onClick,
}: {
  children: ReactNode;
  selected?: boolean;
  dashed?: boolean;
  onRemove?: () => void;
  onClick?: () => void;
}) {
  return (
    <span
      className={`cpv2-tag${selected ? " selected" : ""}${dashed ? " dashed" : ""}`}
      onClick={onClick}
    >
      {children}
      {onRemove && (
        <span
          className="cpv2-tag-x"
          onClick={(e) => { e.stopPropagation(); onRemove(); }}
        >
          ×
        </span>
      )}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Provider dot
// ---------------------------------------------------------------------------

export function ProviderDot({ provider }: { provider: "anthropic" | "openai" | "gemini" | "xai" }) {
  return <span className={`cpv2-provider-dot ${provider}`} />;
}

// ---------------------------------------------------------------------------
// Drill-in row
// ---------------------------------------------------------------------------

export function DrillRow({
  label,
  sublabel,
  icon,
  rightLabel,
  onClick,
}: {
  label: string;
  sublabel?: string;
  icon?: ReactNode;
  rightLabel?: string;
  onClick?: () => void;
}) {
  return (
    <div className="cpv2-drill-row" onClick={onClick}>
      <div className="cpv2-dr-left">
        {icon && <div className="cpv2-dr-icon">{icon}</div>}
        <div>
          <div className="cpv2-dr-label">{label}</div>
          {sublabel && <div className="cpv2-dr-sublabel">{sublabel}</div>}
        </div>
      </div>
      <div className="cpv2-dr-right">
        {rightLabel && <span>{rightLabel}</span>}
        <ChevronRight className="w-3 h-3" />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Mode pills
// ---------------------------------------------------------------------------

export function ModeGroup({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <div className={`cpv2-mode-group ${className}`}>{children}</div>;
}

export function ModePill({
  children,
  active,
  onClick,
  className = "",
}: {
  children: ReactNode;
  active?: boolean;
  onClick?: () => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      className={`cpv2-mode-pill${active ? " active" : ""} ${className}`}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

// ---------------------------------------------------------------------------
// Card list
// ---------------------------------------------------------------------------

export function CardList({ children }: { children: ReactNode }) {
  return <div className="cpv2-card-list">{children}</div>;
}

export function CardItem({
  title,
  actions,
  children,
  className = "",
}: {
  title: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div className={`cpv2-card-item ${className}`}>
      <div className="cpv2-card-header">
        <span className="cpv2-card-title">{title}</span>
        {actions && <div className="cpv2-card-actions">{actions}</div>}
      </div>
      {children && <div className="cpv2-card-sub">{children}</div>}
    </div>
  );
}

export function AddButton({ children, onClick }: { children: ReactNode; onClick?: () => void }) {
  return (
    <button type="button" className="cpv2-add-btn" onClick={onClick}>
      <Plus className="w-3 h-3" />
      {children}
    </button>
  );
}

// ---------------------------------------------------------------------------
// Param group (collapsible)
// ---------------------------------------------------------------------------

export function ParamGroup({
  label,
  preset,
  rightLabel,
  defaultExpanded = false,
  children,
}: {
  label: string;
  preset?: string;
  rightLabel?: string;
  defaultExpanded?: boolean;
  children: ReactNode;
}) {
  const [expanded, setExpanded] = useState(defaultExpanded);

  return (
    <div className="cpv2-param-group">
      <div
        className={`cpv2-param-group-header${expanded ? "" : " collapsed"}`}
        onClick={() => setExpanded(!expanded)}
      >
        <div className="cpv2-pgh-left">
          {expanded
            ? <ChevronDown className="cpv2-pgh-chevron" />
            : <ChevronRight className="cpv2-pgh-chevron" />
          }
          <span className="cpv2-pgh-label">{label}</span>
        </div>
        <div className="cpv2-pgh-right">
          {preset && <span className="cpv2-pgh-preset">{preset}</span>}
          {rightLabel && <span style={{ fontSize: 10, color: "hsl(var(--muted-foreground))" }}>{rightLabel}</span>}
        </div>
      </div>
      {expanded && (
        <div className="cpv2-param-group-body">
          {children}
        </div>
      )}
    </div>
  );
}