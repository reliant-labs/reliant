import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Card } from "../ui/Card";
import { ConfigScopeSelector, getScopeLabel } from "../ui/ConfigScopeSelector";
import type { MCPServerConfig, RecommendedServer } from "../../api/mcp-grpc";
import { ConfigScope } from "../../api/mcp-grpc";

type WizardMode = "install" | "edit";
type HTTPAuthPreset = "none" | "bearer";

type InstallWizardOptions = {
  configOverrides?: Partial<MCPServerConfig>;
};

interface MCPSetupWizardProps {
  mode: WizardMode;
  server: RecommendedServer;
  currentConfig?: Record<string, string>;
  defaultScope?: ConfigScope;
  onInstall: (
    config: Record<string, string>,
    scope: ConfigScope,
    rememberScope: boolean,
    options?: InstallWizardOptions,
  ) => Promise<void>;
  onUpdate: (config: Record<string, string>) => Promise<void>;
  onClose: () => void;
}

export function MCPSetupWizard({
  mode,
  server,
  currentConfig = {},
  defaultScope = ConfigScope.PROJECT,
  onInstall,
  onUpdate,
  onClose,
}: MCPSetupWizardProps) {
  const [values, setValues] = useState<Record<string, string>>(currentConfig);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [scope, setScope] = useState<ConfigScope>(defaultScope);
  const [rememberScope, setRememberScope] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [httpAuthPreset, setHttpAuthPreset] = useState<HTTPAuthPreset>("none");
  const [httpHeaderName, setHTTPHeaderName] = useState("Authorization");
  const [httpHeaderValueTemplate, setHTTPHeaderValueTemplate] = useState("");
  const [httpAuthPresetTouched, setHTTPAuthPresetTouched] = useState(false);

  useEffect(() => {
    setValues(currentConfig);
    setErrors({});
    setSubmitError(null);
  }, [currentConfig, mode, server.name]);

  useEffect(() => {
    setScope(defaultScope);
  }, [defaultScope]);

  const resolvedTransport = useMemo<"stdio" | "http">(() => {
    const transport = server.config?.type?.trim().toLowerCase() || "stdio";
    return transport === "sse" || transport === "http" ? "http" : "stdio";
  }, [server.config?.type]);

  const inferTokenFieldKey = useMemo(() => {
    const fields = server.configFields || [];
    const preferred = fields.find((f) => /token|api[_-]?key/i.test(f.key));
    return preferred?.key || fields[0]?.key || "ACCESS_TOKEN";
  }, [server.configFields]);

  useEffect(() => {
    if (resolvedTransport !== "http") {
      setHttpAuthPreset("none");
      setHTTPHeaderName("Authorization");
      setHTTPHeaderValueTemplate("");
      setHTTPAuthPresetTouched(false);
      return;
    }

    const existingHeaders = Object.entries(server.config.headers || {});
    if (existingHeaders.length > 0) {
      const [key, value] = existingHeaders[0];
      setHttpAuthPreset("bearer");
      setHTTPHeaderName(key);
      setHTTPHeaderValueTemplate(value);
    } else {
      setHttpAuthPreset("none");
      setHTTPHeaderName("Authorization");
      setHTTPHeaderValueTemplate(`Bearer \${${inferTokenFieldKey}}`);
    }

    setHTTPAuthPresetTouched(false);
  }, [resolvedTransport, inferTokenFieldKey, server.name, server.config.headers]);

  const cleanedConfig = useMemo(() => {
    const out: Record<string, string> = {};
    Object.entries(values).forEach(([key, value]) => {
      const trimmed = value.trim();
      if (trimmed) out[key] = trimmed;
    });
    return out;
  }, [values]);

  const validateField = (
    field: NonNullable<RecommendedServer["configFields"]>[number],
    value: string,
  ) => {
    const trimmed = value.trim();

    if (field.required && !trimmed) {
      return `${field.label} is required`;
    }

    if (!trimmed || !field.validationRegex) {
      return null;
    }

    try {
      const regex = new RegExp(field.validationRegex);
      if (!regex.test(trimmed)) {
        return field.validationMessage || `Invalid ${field.label} format`;
      }
    } catch {
      return "This field has an invalid validation rule configured";
    }

    return null;
  };

  const validateAll = () => {
    const nextErrors: Record<string, string> = {};

    server.configFields?.forEach((field) => {
      const error = validateField(field, values[field.key] || "");
      if (error) {
        nextErrors[field.key] = error;
      }
    });

    if (resolvedTransport === "stdio") {
      const command =
        values.command?.trim() || server.config.command?.trim() || "";
      if (!command) {
        nextErrors.command = "Command is required for stdio transport";
      }
    }

    if (resolvedTransport === "http") {
      const url = values.url?.trim() || server.config.url?.trim() || "";
      if (!url) {
        nextErrors.url = "URL is required for HTTP/SSE transport";
      } else if (!/^https?:\/\//i.test(url)) {
        nextErrors.url = "URL must start with http:// or https://";
      }

      if (httpAuthPreset === "bearer") {
        if (!httpHeaderName.trim()) {
          nextErrors.http_header_name =
            "Header name is required for bearer auth";
        }
        if (!httpHeaderValueTemplate.trim()) {
          nextErrors.http_header_value =
            "Header value template is required for bearer auth";
        }
      }
    }

    setErrors(nextErrors);
    return nextErrors;
  };

  const handleSubmit = async () => {
    setSubmitError(null);

    const nextErrors = validateAll();
    if (Object.keys(nextErrors).length > 0) {
      toast.error("Please fix validation errors before continuing");
      return;
    }

    setSubmitting(true);
    try {
      if (mode === "install") {
        let options: InstallWizardOptions | undefined;

        if (resolvedTransport === "http" && httpAuthPresetTouched) {
          const headers: Record<string, string> =
            httpAuthPreset === "bearer"
              ? {
                  [httpHeaderName.trim() || "Authorization"]:
                    httpHeaderValueTemplate.trim(),
                }
              : {};

          options = {
            configOverrides: {
              headers,
            },
          };
        }

        await onInstall(cleanedConfig, scope, rememberScope, options);
      } else {
        await onUpdate(cleanedConfig);
      }

      onClose();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setSubmitError(message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      isOpen={true}
      onClose={onClose}
      size="lg"
      title={`${mode === "install" ? "Install" : "Edit"} ${server.displayName}`}
    >
      <div className="space-y-4">
        <Card size="sm" className="space-y-2" hover={false}>
          <p className="text-sm text-muted-foreground">{server.description}</p>
          {server.setupInstructions && (
            <p className="text-xs text-muted-foreground whitespace-pre-line">
              {server.setupInstructions}
            </p>
          )}
        </Card>

        {mode === "install" && (
          <Card size="sm" className="space-y-3" hover={false}>
            <ConfigScopeSelector
              value={scope}
              onChange={setScope}
              showRememberChoice={true}
              rememberChoice={rememberScope}
              onRememberChoiceChange={setRememberScope}
              label="Install scope"
              helpText="Choose where this MCP configuration should be stored"
            />
          </Card>
        )}

        {mode === "install" && resolvedTransport === "http" && (
          <Card size="sm" className="space-y-3" hover={false}>
            <div className="space-y-1">
              <p className="text-sm font-medium text-foreground">
                Authentication preset
              </p>
              <p className="text-xs text-muted-foreground">
                Choose a standard HTTP auth pattern. This works for any HTTP/SSE
                MCP provider and remains fully editable.
              </p>
            </div>
            <div className="flex gap-2">
              <Button
                type="button"
                size="sm"
                variant={httpAuthPreset === "none" ? "default" : "outline"}
                onClick={() => {
                  setHttpAuthPreset("none");
                  setHTTPAuthPresetTouched(true);
                }}
              >
                No static auth header
              </Button>
              <Button
                type="button"
                size="sm"
                variant={httpAuthPreset === "bearer" ? "default" : "outline"}
                onClick={() => {
                  setHttpAuthPreset("bearer");
                  setHTTPAuthPresetTouched(true);
                }}
              >
                Bearer header
              </Button>
            </div>
            {httpAuthPreset === "bearer" && (
              <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
                <Input
                  label="Header name"
                  value={httpHeaderName}
                  onChange={(e) => {
                    setHTTPHeaderName(e.target.value);
                    setHTTPAuthPresetTouched(true);
                  }}
                  placeholder="Authorization"
                  state={errors.http_header_name ? "error" : "default"}
                  error={errors.http_header_name}
                />
                <Input
                  label="Header value template"
                  value={httpHeaderValueTemplate}
                  onChange={(e) => {
                    setHTTPHeaderValueTemplate(e.target.value);
                    setHTTPAuthPresetTouched(true);
                  }}
                  placeholder={`Bearer \${${inferTokenFieldKey}}`}
                  state={errors.http_header_value ? "error" : "default"}
                  error={errors.http_header_value}
                />
              </div>
            )}
            <div className="rounded-md border border-border/50 bg-background/50 p-3 text-xs text-muted-foreground">
              {httpAuthPreset === "bearer"
                ? "Reliant will set a static header using your template above (for example: Bearer ${API_TOKEN})."
                : "Reliant will not set static auth headers; use provider-native auth flow or unauthenticated endpoint as needed."}
            </div>
          </Card>
        )}

        <Card size="sm" className="space-y-4" hover={false}>
          {server.configFields && server.configFields.length > 0 ? (
            server.configFields.map((field) => (
              <Input
                key={field.key}
                label={field.label}
                type={field.type || "text"}
                value={values[field.key] || ""}
                onChange={(e) => {
                  const next = e.target.value;
                  setValues((prev) => ({ ...prev, [field.key]: next }));
                  if (errors[field.key]) {
                    setErrors((prev) => {
                      const copy = { ...prev };
                      delete copy[field.key];
                      return copy;
                    });
                  }
                }}
                placeholder={field.placeholder}
                description={field.helpText}
                required={field.required}
                state={errors[field.key] ? "error" : "default"}
                error={errors[field.key]}
              />
            ))
          ) : (
            <p className="text-sm text-muted-foreground">
              No required fields. Install with defaults.
            </p>
          )}

          {submitError && (
            <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
              {submitError}
            </div>
          )}

          <div className="rounded-md border border-border/50 bg-background/50 p-3 text-xs text-muted-foreground">
            {mode === "install"
              ? `Scope: ${getScopeLabel(scope)}`
              : "Updating existing server configuration"}
          </div>
        </Card>

        <div className="flex items-center justify-end gap-2 border-t border-border/50 pt-3">
          <Button
            variant="ghost"
            size="sm"
            onClick={onClose}
            disabled={submitting}
          >
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={handleSubmit}
            disabled={submitting}
            loading={submitting}
          >
            {mode === "install" ? "Install" : "Update"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
