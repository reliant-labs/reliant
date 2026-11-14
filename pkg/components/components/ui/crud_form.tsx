import React, { useState } from "react";

type FieldType =
  | "text"
  | "number"
  | "email"
  | "password"
  | "select"
  | "textarea"
  | "checkbox"
  | "date";

interface FormField {
  name: string;
  label: string;
  type: FieldType;
  placeholder?: string;
  required?: boolean;
  options?: Array<{ label: string; value: string }>;
  helpText?: string;
}

interface CrudFormProps {
  fields: FormField[];
  onSubmit: (values: Record<string, unknown>) => void;
  onCancel?: () => void;
  initialValues?: Record<string, unknown>;
  errors?: Record<string, string>;
  loading?: boolean;
  submitLabel?: string;
  title?: string;
  description?: string;
}

export default function CrudForm({
  fields,
  onSubmit,
  onCancel,
  initialValues = {},
  errors = {},
  loading = false,
  submitLabel = "Save",
  title,
  description,
}: CrudFormProps) {
  const [values, setValues] = useState<Record<string, unknown>>(() => {
    const defaults: Record<string, unknown> = {};
    for (const field of fields) {
      defaults[field.name] = initialValues[field.name] ?? (field.type === "checkbox" ? false : "");
    }
    return defaults;
  });

  function handleChange(name: string, value: unknown) {
    setValues((prev) => ({ ...prev, [name]: value }));
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    onSubmit(values);
  }

  return (
    <form
      onSubmit={handleSubmit}
      className="overflow-hidden rounded-xl border border-border bg-surface"
    >
      {(title || description) && (
        <div className="border-b border-border px-6 py-4">
          {title && <h2 className="text-lg font-semibold text-ink">{title}</h2>}
          {description && <p className="mt-1 text-sm text-ink-muted">{description}</p>}
        </div>
      )}

      <div className="space-y-5 px-6 py-5">
        {fields.map((field) => {
          const error = errors[field.name];
          const baseInput =
            "block w-full rounded-lg border px-3 py-2 text-sm transition focus:outline-none focus:ring-1";
          const inputClass = error
            ? `${baseInput} border-danger-border focus:border-danger-border focus:ring-danger`
            : `${baseInput} border-border-strong focus:border-accent-border focus:ring-accent`;

          if (field.type === "checkbox") {
            return (
              <div key={field.name} className="flex items-start gap-3">
                <input
                  type="checkbox"
                  id={field.name}
                  checked={Boolean(values[field.name])}
                  onChange={(e) => handleChange(field.name, e.target.checked)}
                  className="mt-1 h-4 w-4 rounded border-border-strong text-accent focus:ring-accent"
                />
                <div>
                  <label htmlFor={field.name} className="text-sm font-medium text-ink-muted">
                    {field.label}
                  </label>
                  {field.helpText && <p className="text-xs text-ink-muted">{field.helpText}</p>}
                </div>
              </div>
            );
          }

          return (
            <div key={field.name}>
              <label htmlFor={field.name} className="mb-1 block text-sm font-medium text-ink-muted">
                {field.label}
                {field.required && <span className="ml-0.5 text-danger">*</span>}
              </label>

              {field.type === "textarea" ? (
                <textarea
                  id={field.name}
                  value={String(values[field.name] ?? "")}
                  onChange={(e) => handleChange(field.name, e.target.value)}
                  placeholder={field.placeholder}
                  required={field.required}
                  rows={4}
                  className={inputClass}
                />
              ) : field.type === "select" ? (
                <select
                  id={field.name}
                  value={String(values[field.name] ?? "")}
                  onChange={(e) => handleChange(field.name, e.target.value)}
                  required={field.required}
                  className={inputClass}
                >
                  <option value="">{field.placeholder ?? "Select..."}</option>
                  {field.options?.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {opt.label}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  id={field.name}
                  type={field.type}
                  value={String(values[field.name] ?? "")}
                  onChange={(e) =>
                    handleChange(
                      field.name,
                      field.type === "number" ? Number(e.target.value) : e.target.value,
                    )
                  }
                  placeholder={field.placeholder}
                  required={field.required}
                  className={inputClass}
                />
              )}

              {field.helpText && !error && (
                <p className="mt-1 text-xs text-ink-muted">{field.helpText}</p>
              )}
              {error && <p className="mt-1 text-xs text-danger">{error}</p>}
            </div>
          );
        })}
      </div>

      {/* Actions */}
      <div className="flex items-center justify-end gap-3 border-t border-border bg-surface-muted px-6 py-4">
        {onCancel && (
          <button
            type="button"
            onClick={onCancel}
            disabled={loading}
            className="rounded-lg border border-border-strong bg-surface px-4 py-2 text-sm font-medium text-ink-muted transition hover:bg-surface-muted disabled:opacity-50"
          >
            Cancel
          </button>
        )}
        <button
          type="submit"
          disabled={loading}
          className="inline-flex items-center gap-2 rounded-lg bg-accent px-4 py-2 text-sm font-medium text-on-accent transition hover:bg-accent-hover disabled:opacity-50"
        >
          {loading && (
            <svg
              className="h-4 w-4 animate-spin motion-reduce:[animation-duration:3s]"
              viewBox="0 0 24 24"
              fill="none"
            >
              <circle
                cx="12"
                cy="12"
                r="10"
                stroke="currentColor"
                strokeWidth="4"
                className="opacity-25"
              />
              <path
                d="M4 12a8 8 0 018-8v4a4 4 0 00-4 4H4z"
                fill="currentColor"
                className="opacity-75"
              />
            </svg>
          )}
          {submitLabel}
        </button>
      </div>
    </form>
  );
}
