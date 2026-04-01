import { useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Textarea } from "../ui/Textarea";
import { ConfigScopeSelector } from "../ui/ConfigScopeSelector";
import { ConfigScope } from "../../api/mcp-grpc";
import { toast } from "sonner";

interface MCPCustomServerModalProps {
  defaultScope?: ConfigScope;
  onClose: () => void;
  onSave: (
    name: string,
    config: {
      command: string;
      args?: string[];
      env?: string[];
      headers?: Record<string, string>;
      type: string;
      url?: string;
    },
    scope: ConfigScope,
    rememberScope: boolean,
  ) => Promise<void>;
}

type ArgRow = { id: string; value: string };
type EnvVarRow = { id: string; key: string; value: string };
type EntryMode = "form" | "json";
type HeaderRow = { id: string; key: string; value: string };
type MCPConfigPayload = {
  command: string;
  args?: string[];
  env?: string[];
  headers?: Record<string, string>;
  type: string;
  url?: string;
};

const createRowId = () =>
  `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;

const splitCommandTokens = (value: string): string[] => {
  const tokens: string[] = [];
  const regex = /"([^"]*)"|'([^']*)'|[^\s"']+/g;
  for (const match of value.matchAll(regex)) {
    tokens.push(match[1] ?? match[2] ?? match[0]);
  }
  return tokens;
};
const newEmptyArgRow = (): ArgRow => ({ id: createRowId(), value: "" });
const newEmptyEnvVarRow = (): EnvVarRow => ({
  id: createRowId(),
  key: "",
  value: "",
});

const validateServerName = (value: string): string | null => {
  if (!value.trim()) return "Server name is required";
  if (!/^[a-zA-Z0-9._-]+$/.test(value.trim())) {
    return "Server name can only contain letters, numbers, dot, underscore, and dash";
  }
  return null;
};

export function MCPCustomServerModal({
  defaultScope = ConfigScope.PROJECT,
  onClose,
  onSave,
}: MCPCustomServerModalProps) {
  const [mode, setMode] = useState<EntryMode>("form");
  const [name, setName] = useState("");
  const [transportType, setTransportType] = useState<"stdio" | "http">("stdio");
  const [command, setCommand] = useState("");
  const [url, setURL] = useState("");
  const [args, setArgs] = useState<ArgRow[]>(() => [newEmptyArgRow()]);
  const [envVars, setEnvVars] = useState<EnvVarRow[]>(() => [
    newEmptyEnvVarRow(),
  ]);
  const [headers, setHeaders] = useState<HeaderRow[]>(() => [
    newEmptyEnvVarRow(),
  ]);
  const [jsonSnippet, setJsonSnippet] = useState("");
  const [scope, setScope] = useState<ConfigScope>(defaultScope);
  const [rememberScope, setRememberScope] = useState(false);
  const [saving, setSaving] = useState(false);

  const addArg = () => setArgs((prev) => [...prev, newEmptyArgRow()]);
  const updateArg = (id: string, value: string) => {
    setArgs((prev) =>
      prev.map((arg) => (arg.id === id ? { ...arg, value } : arg)),
    );
  };
  const removeArg = (id: string) => {
    setArgs((prev) => {
      if (prev.length <= 1) return [newEmptyArgRow()];
      return prev.filter((arg) => arg.id !== id);
    });
  };

  const addEnvVar = () => setEnvVars((prev) => [...prev, newEmptyEnvVarRow()]);
  const updateEnvVar = (id: string, field: "key" | "value", value: string) => {
    setEnvVars((prev) =>
      prev.map((envVar) =>
        envVar.id === id ? { ...envVar, [field]: value } : envVar,
      ),
    );
  };
  const removeEnvVar = (id: string) => {
    setEnvVars((prev) => {
      if (prev.length <= 1) return [newEmptyEnvVarRow()];
      return prev.filter((envVar) => envVar.id !== id);
    });
  };

  const addHeader = () => setHeaders((prev) => [...prev, newEmptyEnvVarRow()]);
  const updateHeader = (id: string, field: "key" | "value", value: string) => {
    setHeaders((prev) =>
      prev.map((header) =>
        header.id === id ? { ...header, [field]: value } : header,
      ),
    );
  };
  const removeHeader = (id: string) => {
    setHeaders((prev) => {
      if (prev.length <= 1) return [newEmptyEnvVarRow()];
      return prev.filter((header) => header.id !== id);
    });
  };

  const getNormalizedEnv = () =>
    envVars
      .map((envVar) => ({ key: envVar.key.trim(), value: envVar.value }))
      .filter((envVar) => envVar.key.length > 0)
      .map((envVar) => `${envVar.key}=${envVar.value}`);

  const getNormalizedHeaders = () => {
    const out: Record<string, string> = {};
    headers
      .map((header) => ({ key: header.key.trim(), value: header.value }))
      .filter((header) => header.key.length > 0)
      .forEach((header) => {
        out[header.key] = header.value;
      });
    return out;
  };

  const validateForm = (): string | null => {
    const nameError = validateServerName(name);
    if (nameError) return nameError;

    if (transportType === "stdio" && !command.trim()) {
      return "Command is required for stdio servers";
    }
    if (transportType === "http" && !url.trim()) {
      return "URL is required for HTTP/SSE servers";
    }
    if (transportType === "http" && !/^https?:\/\//.test(url.trim())) {
      return "URL must start with http:// or https://";
    }

    for (const envVar of envVars) {
      const key = envVar.key.trim();
      const hasValue = envVar.value.length > 0;

      if (!key && !hasValue) continue;
      if (!key) {
        return "Environment variable key is required when a value is provided";
      }
      if (!/^[^\s=]+$/.test(key)) {
        return `Invalid environment variable key: ${key}`;
      }
    }

    for (const header of headers) {
      const key = header.key.trim();
      const hasValue = header.value.length > 0;

      if (!key && !hasValue) continue;
      if (!key) {
        return "Header key is required when a header value is provided";
      }
      if (/\s/.test(key)) {
        return `Invalid header key: ${key}`;
      }
    }

    return null;
  };

  const buildConfigFromForm = (): {
    resolvedName: string;
    config: MCPConfigPayload;
  } => {
    const normalizedArgs = args.map((a) => a.value.trim()).filter(Boolean);
    const normalizedEnv = getNormalizedEnv();
    const normalizedHeaders = getNormalizedHeaders();

    const config: MCPConfigPayload = {
      command: command.trim(),
      type: transportType,
    };

    if (transportType === "stdio" && normalizedArgs.length > 0)
      config.args = normalizedArgs;
    if (normalizedEnv.length > 0) config.env = normalizedEnv;
    if (Object.keys(normalizedHeaders).length > 0)
      config.headers = normalizedHeaders;

    if (transportType === "http") {
      config.url = url.trim();
      config.command = "";
    }

    return { resolvedName: name.trim(), config };
  };

  const buildConfigFromJsonSnippet = (): {
    resolvedName: string;
    config: MCPConfigPayload;
  } => {
    if (!jsonSnippet.trim()) {
      throw new Error("JSON snippet is required");
    }

    let parsed: unknown;
    try {
      parsed = JSON.parse(jsonSnippet);
    } catch {
      throw new Error("Invalid JSON snippet");
    }

    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      throw new Error("JSON snippet must be an object");
    }

    const parsedObj = parsed as Record<string, unknown>;

    let resolvedName = name.trim();
    let rawConfig: unknown = parsedObj;

    if (
      parsedObj.mcpServers &&
      typeof parsedObj.mcpServers === "object" &&
      !Array.isArray(parsedObj.mcpServers)
    ) {
      const entries = Object.entries(
        parsedObj.mcpServers as Record<string, unknown>,
      );
      if (entries.length !== 1) {
        throw new Error("mcpServers snippet must contain exactly one server");
      }
      resolvedName = entries[0][0];
      rawConfig = entries[0][1];
    }

    const nameError = validateServerName(resolvedName);
    if (nameError) throw new Error(nameError);

    if (
      !rawConfig ||
      typeof rawConfig !== "object" ||
      Array.isArray(rawConfig)
    ) {
      throw new Error("Server config in JSON snippet must be an object");
    }

    const configObj = rawConfig as Record<string, unknown>;
    const rawType =
      typeof configObj.type === "string"
        ? configObj.type.toLowerCase().trim()
        : "stdio";
    const type = rawType === "sse" || rawType === "http" ? "http" : "stdio";

    const config: MCPConfigPayload = {
      command: typeof configObj.command === "string" ? configObj.command : "",
      type,
    };

    if (type === "stdio" && !config.command.trim()) {
      throw new Error("Command is required for stdio servers");
    }

    if (type === "http") {
      const parsedURL =
        typeof configObj.url === "string" ? configObj.url.trim() : "";
      if (!parsedURL) {
        throw new Error("URL is required for HTTP/SSE servers");
      }
      if (!/^https?:\/\//.test(parsedURL)) {
        throw new Error("URL must start with http:// or https://");
      }
      config.url = parsedURL;
      config.command = "";
    }

    if (configObj.args !== undefined) {
      if (
        !Array.isArray(configObj.args) ||
        !configObj.args.every((arg) => typeof arg === "string")
      ) {
        throw new Error("args must be an array of strings");
      }
      if (configObj.args.length > 0) {
        config.args = configObj.args;
      }
    }

    // Support snippets that provide command + args in a single string, e.g.
    // "command": "npx -y mcp-remote ...".
    // Convert to MCP's expected shape: command="npx", args=["-y", ...].
    if (type === "stdio" && config.command.trim().includes(" ")) {
      const tokens = splitCommandTokens(config.command.trim());
      if (tokens.length > 0) {
        config.command = tokens[0];
        const inlineArgs = tokens.slice(1);
        if (inlineArgs.length > 0) {
          config.args = [...inlineArgs, ...(config.args ?? [])];
        }
      }
    }

    if (configObj.env !== undefined) {
      if (Array.isArray(configObj.env)) {
        if (!configObj.env.every((entry) => typeof entry === "string")) {
          throw new Error(
            "env array entries must be strings in KEY=VALUE format",
          );
        }
        if (configObj.env.length > 0) {
          config.env = configObj.env;
        }
      } else if (configObj.env && typeof configObj.env === "object") {
        const envPairs = Object.entries(
          configObj.env as Record<string, unknown>,
        );
        const normalized = envPairs.map(
          ([key, value]) => `${key}=${String(value ?? "")}`,
        );
        if (normalized.length > 0) {
          config.env = normalized;
        }
      } else {
        throw new Error(
          "env must be either an object map or an array of KEY=VALUE strings",
        );
      }
    }

    if (configObj.headers !== undefined) {
      if (
        !configObj.headers ||
        typeof configObj.headers !== "object" ||
        Array.isArray(configObj.headers)
      ) {
        throw new Error("headers must be an object map");
      }
      const headerPairs = Object.entries(
        configObj.headers as Record<string, unknown>,
      );
      const normalizedHeaders: Record<string, string> = {};
      for (const [key, value] of headerPairs) {
        normalizedHeaders[key] = String(value ?? "");
      }
      if (Object.keys(normalizedHeaders).length > 0) {
        config.headers = normalizedHeaders;
      }
    }

    return { resolvedName, config };
  };

  const handleSave = async () => {
    let resolvedName = "";
    let config: MCPConfigPayload | null = null;

    try {
      if (mode === "form") {
        const validationError = validateForm();
        if (validationError) {
          toast.error(validationError);
          return;
        }

        const built = buildConfigFromForm();
        resolvedName = built.resolvedName;
        config = built.config;
      } else {
        const built = buildConfigFromJsonSnippet();
        resolvedName = built.resolvedName;
        config = built.config;
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      toast.error(msg);
      return;
    }

    setSaving(true);
    try {
      await onSave(resolvedName, config, scope, rememberScope);
      onClose();
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      toast.error(`Failed to add custom MCP server: ${msg}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      isOpen={true}
      onClose={onClose}
      title="Add Custom MCP Server"
      size="lg"
    >
      <div className="space-y-4">
        <div className="space-y-2">
          <label className="block text-sm font-medium text-foreground">
            Entry Mode
          </label>
          <div className="flex gap-2">
            <Button
              size="sm"
              variant={mode === "form" ? "default" : "outline"}
              onClick={() => setMode("form")}
            >
              Guided Form
            </Button>
            <Button
              size="sm"
              variant={mode === "json" ? "default" : "outline"}
              onClick={() => setMode("json")}
            >
              JSON Snippet
            </Button>
          </div>
        </div>

        {mode === "form" ? (
          <>
            <Input
              label="Server Name"
              placeholder="my-custom-mcp"
              value={name}
              onChange={(e) => setName(e.target.value)}
              description="Unique identifier used in tool names"
              required
            />

            <div className="space-y-2">
              <label className="block text-sm font-medium text-foreground">
                Transport Type
              </label>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant={transportType === "stdio" ? "default" : "outline"}
                  onClick={() => setTransportType("stdio")}
                >
                  stdio
                </Button>
                <Button
                  size="sm"
                  variant={transportType === "http" ? "default" : "outline"}
                  onClick={() => setTransportType("http")}
                >
                  http/sse
                </Button>
              </div>
            </div>

            {transportType === "stdio" ? (
              <>
                <Input
                  label="Command"
                  placeholder="npx"
                  value={command}
                  onChange={(e) => setCommand(e.target.value)}
                  required
                />

                <div className="space-y-2">
                  <label className="block text-sm font-medium text-foreground">
                    Args
                  </label>
                  {args.map((arg, index) => (
                    <div key={arg.id} className="flex items-center gap-2">
                      <Input
                        placeholder={index === 0 ? "-y" : "argument"}
                        value={arg.value}
                        onChange={(e) => updateArg(arg.id, e.target.value)}
                        className="flex-1"
                      />
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        onClick={() => removeArg(arg.id)}
                        className="whitespace-nowrap"
                      >
                        <span className="inline-flex items-center gap-1">
                          <Trash2 className="w-4 h-4" />
                          Remove
                        </span>
                      </Button>
                    </div>
                  ))}
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={addArg}
                    className="whitespace-nowrap"
                  >
                    <span className="inline-flex items-center gap-1">
                      <Plus className="w-4 h-4" />
                      Add Arg
                    </span>
                  </Button>
                </div>
              </>
            ) : (
              <Input
                label="HTTP/SSE URL"
                placeholder="https://example.com/mcp"
                value={url}
                onChange={(e) => setURL(e.target.value)}
                required
              />
            )}

            <div className="space-y-2">
              <label className="block text-sm font-medium text-foreground">
                Environment Variables
              </label>
              {envVars.map((envVar, index) => (
                <div key={envVar.id} className="flex items-center gap-2">
                  <Input
                    placeholder={index === 0 ? "API_KEY" : "KEY"}
                    value={envVar.key}
                    onChange={(e) =>
                      updateEnvVar(envVar.id, "key", e.target.value)
                    }
                    className="flex-1 font-mono"
                  />
                  <Input
                    placeholder={index === 0 ? "your_key" : "value"}
                    value={envVar.value}
                    onChange={(e) =>
                      updateEnvVar(envVar.id, "value", e.target.value)
                    }
                    className="flex-1 font-mono"
                  />
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => removeEnvVar(envVar.id)}
                    className="whitespace-nowrap"
                  >
                    <span className="inline-flex items-center gap-1">
                      <Trash2 className="w-4 h-4" />
                      Remove
                    </span>
                  </Button>
                </div>
              ))}
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={addEnvVar}
                className="whitespace-nowrap"
              >
                <span className="inline-flex items-center gap-1">
                  <Plus className="w-4 h-4" />
                  Add Env Var
                </span>
              </Button>
            </div>
          </>
        ) : (
          <>
            <Input
              label="Server Name (optional)"
              placeholder="my-custom-mcp"
              value={name}
              onChange={(e) => setName(e.target.value)}
              description="Optional when snippet includes mcpServers with a single named server"
            />

            <div className="space-y-2">
              <label className="block text-sm font-medium text-foreground">
                JSON Snippet
              </label>
              <Textarea
                value={jsonSnippet}
                onChange={(e) => setJsonSnippet(e.target.value)}
                className="min-h-[220px] font-mono text-xs"
                placeholder={`{
  "mcpServers": {
    "my-custom-mcp": {
      "command": "npx",
      "args": ["-y", "@scope/server"],
      "env": { "API_KEY": "${"${API_KEY}"}" },
      "type": "stdio"
    }
  }
}`}
              />
              <p className="text-xs text-muted-foreground">
                Supports either a full{" "}
                <code className="font-mono">mcpServers</code> snippet or a
                direct server config object.
              </p>
            </div>
          </>
        )}

        <div className="space-y-2">
          <label className="block text-sm font-medium text-foreground">
            HTTP Headers
          </label>
          {headers.map((header, index) => (
            <div key={header.id} className="flex items-center gap-2">
              <Input
                placeholder={index === 0 ? "Authorization" : "Header-Name"}
                value={header.key}
                onChange={(e) => updateHeader(header.id, "key", e.target.value)}
                className="flex-1 font-mono"
              />
              <Input
                placeholder={index === 0 ? "Bearer ${TOKEN}" : "value"}
                value={header.value}
                onChange={(e) =>
                  updateHeader(header.id, "value", e.target.value)
                }
                className="flex-1 font-mono"
              />
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => removeHeader(header.id)}
                className="whitespace-nowrap"
              >
                <span className="inline-flex items-center gap-1">
                  <Trash2 className="w-4 h-4" />
                  Remove
                </span>
              </Button>
            </div>
          ))}
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={addHeader}
            className="whitespace-nowrap"
          >
            <span className="inline-flex items-center gap-1">
              <Plus className="w-4 h-4" />
              Add Header
            </span>
          </Button>
        </div>

        <ConfigScopeSelector
          value={scope}
          onChange={setScope}
          showRememberChoice={true}
          rememberChoice={rememberScope}
          onRememberChoiceChange={setRememberScope}
          label="Save configuration to"
          helpText="Choose where to store this custom MCP server configuration"
        />

        <div className="flex justify-end gap-2 pt-2">
          <Button variant="outline" onClick={onClose} size="md">
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={handleSave}
            loading={saving}
            disabled={saving}
            size="md"
          >
            Add Server
          </Button>
        </div>
      </div>
    </Modal>
  );
}
