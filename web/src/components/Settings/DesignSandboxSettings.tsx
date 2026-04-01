import { DesignSandbox } from "../Dev/DesignSandbox";

/**
 * DesignSandboxSettings - Wrapper for DesignSandbox in settings
 *
 * Dev-only settings page that provides access to the Design Sandbox
 * for prototyping UI components.
 */
export function DesignSandboxSettings() {
  return (
    <div className="h-full">
      <DesignSandbox />
    </div>
  );
}
