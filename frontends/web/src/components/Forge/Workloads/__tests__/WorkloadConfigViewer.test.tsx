// Copyright (c) 2025 Reliant Labs
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import type { ConfigReader, WorkloadConfig } from "@/services/forge/workloadConfig";
import { WorkloadConfigViewer } from "../WorkloadConfigViewer";

const props = { project: "project", environmentId: "env-prod", environmentName: "prod", workload: "api" };
const result = (overrides: Partial<WorkloadConfig> = {}): WorkloadConfig => ({
  environmentId: "env-prod", workload: "api", bundleId: "bundle-current", bundleDigest: `sha256:${"b".repeat(64)}`, releaseVersion: "v2", objects: [{ kind: "ConfigMap", metadata: { name: "api" }, data: { MODE: "prod", TOKEN: "forge.dev/redacted: withheld" } }],
  renderedYaml: 'data:\n  html: "<img src=x onerror=alert(1)>"\n  TOKEN: "forge.dev/redacted: withheld"',
  frontendConfigUnsupportedReason: "Verified static artifact resolver is not supported.", ...overrides,
});
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const view = () => userEvent.click(screen.getByRole("button", { name: "View config values" }));

describe("explicit configuration values", () => {
  it("does not request/prefetch until clicked; uses no selector for current promotion and renders only text", async () => {
    const user = userEvent.setup();
    const read = vi.fn<ConfigReader>().mockResolvedValue(result());
    const { container } = render(<WorkloadConfigViewer {...props} readConfig={read} />);
    expect(read).not.toHaveBeenCalled();
    await view();
    expect(read).toHaveBeenCalledWith({ environmentId: "env-prod", workload: "api" }, expect.any(AbortSignal));
    expect(await screen.findByLabelText("Server-redacted manifest YAML")).toHaveTextContent("<img src=x onerror=alert(1)>");
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByText(/Verified static artifact resolver/)).toBeInTheDocument();
    expect(screen.getByText("bundle-current")).toBeInTheDocument();
    expect(screen.getByText(`sha256:${"b".repeat(64)}`)).toBeInTheDocument();
    const write = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue();
    await user.click(screen.getByRole("button", { name: "Copy redacted YAML" }));
    expect(write).toHaveBeenCalledWith(result().renderedYaml);
    await userEvent.click(screen.getByRole("button", { name: "Hide values" }));
    expect(screen.queryByLabelText("Server-redacted manifest YAML")).toBeNull();
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("downloads only the server-redacted bytes and handles clipboard failure honestly", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(new Error("unavailable"));
    const read = vi.fn<ConfigReader>().mockResolvedValue(result());
    render(<WorkloadConfigViewer {...props} readConfig={read} />);
    await view();
    await screen.findByLabelText("Server-redacted manifest YAML");
    await user.click(screen.getByRole("button", { name: "Copy redacted YAML" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Clipboard unavailable");
    const create = vi.fn().mockReturnValue("blob:redacted-fixture");
    const revoke = vi.fn();
    const oldCreate = URL.createObjectURL;
    const oldRevoke = URL.revokeObjectURL;
    URL.createObjectURL = create;
    URL.revokeObjectURL = revoke;
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    try {
      await user.click(screen.getByRole("button", { name: "Download redacted YAML" }));
      const blob = create.mock.calls[0]?.[0] as Blob;
      expect(blob.type).toBe("text/yaml;charset=utf-8");
      const reader = new FileReader();
      const text = new Promise((resolve) => { reader.onload = () => resolve(reader.result); });
      reader.readAsText(blob);
      expect(await text).toBe(result().renderedYaml);
      expect(click).toHaveBeenCalledOnce();
      expect(revoke).toHaveBeenCalledWith("blob:redacted-fixture");
      expect(read).toHaveBeenCalledOnce();
    } finally {
      URL.createObjectURL = oldCreate;
      URL.revokeObjectURL = oldRevoke;
      click.mockRestore();
    }
  });

  it.each([
    [Code.PermissionDenied, "deploy:config:read"],
    [Code.Unimplemented, "does not support configuration values yet"],
    [Code.NotFound, "another environment"],
  ])("explains refusal %s without echoing server bytes and keeps the names-only path", async (code, message) => {
    const read = vi.fn<ConfigReader>().mockRejectedValue(new ConnectError("sensitive server bytes", code));
    render(<WorkloadConfigViewer {...props} readConfig={read} />);
    await view();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(message);
    expect(alert).not.toHaveTextContent("sensitive server bytes");
    expect(alert).toHaveTextContent(/names-only|No configuration/);
    expect(screen.queryByRole("button", { name: "Copy redacted YAML" })).toBeNull();
  });

  it("rejects cross-environment responses and never offers export", async () => {
    const read = vi.fn<ConfigReader>().mockResolvedValue(result({ environmentId: "env-other" }));
    render(<WorkloadConfigViewer {...props} readConfig={read} />);
    await view();
    await screen.findByRole("alert");
    expect(screen.queryByLabelText("Server-redacted manifest YAML")).toBeNull();
  });

  it("compares with exactly two explicit authorized reads pinned to the displayed bundle", async () => {
    const historical = result({ bundleId: "bundle-old", releaseVersion: "v1", objects: [{ kind: "ConfigMap", metadata: { name: "api" }, data: { MODE: "dev", TOKEN: "forge.dev/redacted: withheld" } }] });
    const read = vi.fn<ConfigReader>().mockResolvedValueOnce(result()).mockResolvedValueOnce(result()).mockResolvedValueOnce(historical);
    render(<WorkloadConfigViewer {...props} readConfig={read} />);
    await view();
    await screen.findByLabelText("Server-redacted manifest YAML");
    await userEvent.type(screen.getByRole("textbox", { name: "Historical release version" }), "v1");
    expect(read).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "Compare redacted values" }));
    const comparison = await screen.findByRole("region", { name: "Redacted configuration comparison" });
    expect(read).toHaveBeenCalledTimes(3);
    expect(read.mock.calls[1]?.[0]).toEqual({ environmentId: "env-prod", workload: "api", bundleId: "bundle-current" });
    expect(read.mock.calls[2]?.[0]).toEqual({ environmentId: "env-prod", workload: "api", releaseVersion: "v1" });
    expect(comparison).toHaveTextContent("secret value not compared");
    expect(comparison).toHaveTextContent('Historical: "dev"');
    expect(comparison).toHaveTextContent('Selected: "prod"');
  });

  it("aborts both comparison reads on cancel and ignores even late successful completions", async () => {
    const a = deferred<WorkloadConfig>();
    const b = deferred<WorkloadConfig>();
    const read = vi.fn<ConfigReader>().mockResolvedValueOnce(result()).mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    render(<WorkloadConfigViewer {...props} readConfig={read} />);
    await view();
    await screen.findByLabelText("Server-redacted manifest YAML");
    await userEvent.type(screen.getByRole("textbox"), "v1");
    await userEvent.click(screen.getByRole("button", { name: "Compare redacted values" }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel config read" }));
    expect(read.mock.calls[1]?.[1].aborted).toBe(true);
    expect(read.mock.calls[2]?.[1].aborted).toBe(true);
    await act(async () => { a.resolve(result()); b.resolve(result({ bundleId: "old", releaseVersion: "v1" })); });
    expect(screen.queryByLabelText("Server-redacted manifest YAML")).toBeNull();
    expect(screen.queryByRole("region")).toBeNull();
  });

  it("clears values and aborts on env/project changes, does not automatically fetch the new scope", async () => {
    const late = deferred<WorkloadConfig>();
    const read = vi.fn<ConfigReader>().mockReturnValueOnce(late.promise).mockResolvedValue(result({ environmentId: "env-dev" }));
    const { rerender } = render(<WorkloadConfigViewer {...props} readConfig={read} />);
    await view();
    rerender(<WorkloadConfigViewer {...props} environmentId="env-dev" environmentName="dev" readConfig={read} />);
    expect(read.mock.calls[0]?.[1].aborted).toBe(true);
    await act(async () => late.resolve(result()));
    expect(read).toHaveBeenCalledTimes(1);
    expect(screen.queryByLabelText("Server-redacted manifest YAML")).toBeNull();
    await view();
    await screen.findByLabelText("Server-redacted manifest YAML");
    rerender(<WorkloadConfigViewer {...props} environmentId="env-dev" project="other-project" readConfig={read} />);
    expect(screen.queryByLabelText("Server-redacted manifest YAML")).toBeNull();
    expect(read).toHaveBeenCalledTimes(2);
  });

  it("pins organization on explicit reads and clears/aborts immediately when it changes", async () => {
    const late = deferred<WorkloadConfig>();
    const read = vi.fn<ConfigReader>().mockReturnValueOnce(late.promise).mockResolvedValue(result());
    const { rerender } = render(<WorkloadConfigViewer {...props} orgId="org-one" readConfig={read} />);
    expect(read).not.toHaveBeenCalled();
    await view();
    expect(read.mock.calls[0]?.[0]).toEqual({ environmentId: "env-prod", workload: "api", orgId: "org-one" });
    rerender(<WorkloadConfigViewer {...props} orgId="org-two" readConfig={read} />);
    expect(read.mock.calls[0]?.[1].aborted).toBe(true);
    await act(async () => late.resolve(result()));
    expect(screen.queryByLabelText("Server-redacted manifest YAML")).toBeNull();
    expect(read).toHaveBeenCalledTimes(1);
    await view();
    await screen.findByLabelText("Server-redacted manifest YAML");
    expect(read.mock.calls[1]?.[0]).toEqual({ environmentId: "env-prod", workload: "api", orgId: "org-two" });
    rerender(<WorkloadConfigViewer {...props} orgId="org-three" readConfig={read} />);
    expect(screen.queryByLabelText("Server-redacted manifest YAML")).toBeNull();
    expect(read).toHaveBeenCalledTimes(2);
  });

  it("aborts the peer read and shows no partial values when one comparison call refuses", async () => {
    const peer = deferred<WorkloadConfig>();
    const read = vi.fn<ConfigReader>().mockResolvedValueOnce(result()).mockReturnValueOnce(peer.promise).mockRejectedValueOnce(new ConnectError("denied", Code.PermissionDenied));
    render(<WorkloadConfigViewer {...props} readConfig={read} />);
    await view();
    await screen.findByLabelText("Server-redacted manifest YAML");
    await userEvent.type(screen.getByRole("textbox"), "v1");
    await userEvent.click(screen.getByRole("button", { name: "Compare redacted values" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("deploy:config:read"));
    expect(read.mock.calls[1]?.[1].aborted).toBe(true);
    expect(screen.queryByLabelText("Server-redacted manifest YAML")).toBeNull();
  });
});
