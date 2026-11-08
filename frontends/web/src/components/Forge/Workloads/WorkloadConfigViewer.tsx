// Copyright (c) 2025 Reliant Labs

import { useEffect, useRef, useState } from "react";

import { configReadError, getWorkloadConfig, validateWorkloadConfig, type ConfigReader, type WorkloadConfig, type WorkloadConfigRequest } from "@/services/forge/workloadConfig";

import { BUTTON_PRIMARY, BUTTON_SECONDARY, Note, Panel } from "../Releases/releaseParts";
import { configRows, diffConfig, type ConfigDiffRow } from "./workloadConfigModel";

interface Props {
  orgId?: string;
  project: string;
  environmentId: string;
  environmentName: string;
  workload: string;
  /** Fixture/transport seam. No registry URL or artifact-fetch seam. */
  readConfig?: ConfigReader;
}

/** Keyed boundary discards sensitive state immediately on project/env/workload change. */
export function WorkloadConfigViewer(props: Props) {
  return <ConfigSession key={JSON.stringify([props.orgId, props.project, props.environmentId, props.workload])} {...props} />;
}

function ConfigSession({ orgId, environmentId, environmentName, workload, readConfig = getWorkloadConfig }: Props) {
  const [config, setConfig] = useState<WorkloadConfig | null>(null);
  const [comparison, setComparison] = useState<{ historical: WorkloadConfig; current: WorkloadConfig; rows: ConfigDiffRow[] } | null>(null);
  const [release, setRelease] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copyState, setCopyState] = useState("");
  const pending = useRef<AbortController | null>(null);
  const generation = useRef(0);
  useEffect(() => () => { generation.current++; pending.current?.abort(); }, []);

  const cancel = () => {
    generation.current++;
    pending.current?.abort();
    pending.current = null;
    setBusy(false);
  };

  const load = async (historicalRelease?: string) => {
    cancel();
    const token = generation.current;
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true);
    setError(null);
    setComparison(null);
    setCopyState("");
    // Clear an earlier projection on a fresh read/refusal: it must not appear
    // to be the answer to a new request whose authorization failed.
    const pinned = config?.bundleId;
    setConfig(null);
    const read = async (request: WorkloadConfigRequest) => validateWorkloadConfig(await readConfig(request, controller.signal), request);
    try {
      const scope = { environmentId, workload, ...(orgId ? { orgId } : {}) };
      if (historicalRelease && pinned) {
        // TWO independently authorized, audited reads. Pin the visible bundle,
        // not a promotion that might move while the comparison is loading.
        const [current, historical] = await Promise.all([
          read({ ...scope, bundleId: pinned }),
          read({ ...scope, releaseVersion: historicalRelease }),
        ]);
        if (generation.current !== token || controller.signal.aborted) return;
        setConfig(current);
        setComparison({ historical, current, rows: diffConfig(historical.objects, current.objects) });
      } else {
        // Absence of both selectors means EXACT current promotion on the server.
        const result = await read(scope);
        if (generation.current !== token || controller.signal.aborted) return;
        setConfig(result);
      }
    } catch (failure) {
      if (generation.current === token && !controller.signal.aborted) {
        controller.abort(); // stop the other comparison read if either failed
        setError(configReadError(failure));
      }
    } finally {
      if (generation.current === token) setBusy(false);
    }
  };

  const copy = async () => {
    if (!config) return;
    const token = generation.current;
    try {
      await navigator.clipboard.writeText(config.renderedYaml);
      if (generation.current === token) setCopyState("Redacted YAML copied.");
    } catch {
      if (generation.current === token) setCopyState("Clipboard unavailable. Select and copy the redacted YAML below.");
    }
  };
  const download = () => {
    if (!config) return;
    const url = URL.createObjectURL(new Blob([config.renderedYaml], { type: "text/yaml;charset=utf-8" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = "workload-config.redacted.yaml";
    anchor.click();
    URL.revokeObjectURL(url);
  };

  return (
    <Panel title="Config values" meta="On demand · server-redacted · audited" testId="workload-config-values">
      <div className="space-y-4">
        <p className="text-sm text-ink-muted">Values require deploy:read and deploy:config:read. Each read is authorized and audited by the server. Secret values are withheld; references are shown by name. The names-only view below needs only deploy:read.</p>
        <div className="flex flex-wrap items-center gap-2">
          <button type="button" className={BUTTON_PRIMARY} disabled={busy} onClick={() => void load()}>{config ? "Read current promotion again" : "View config values"}</button>
          {busy && <button type="button" className={BUTTON_SECONDARY} onClick={cancel}>Cancel config read</button>}
          {config && <button type="button" className={BUTTON_SECONDARY} onClick={() => { cancel(); setConfig(null); setComparison(null); setError(null); setCopyState(""); }}>Hide values</button>}
        </div>
        {busy && <p role="status" className="text-sm text-ink-muted">Reading redacted configuration for {workload} in {environmentName}…</p>}
        {error && <div role="alert"><Note tone="neutral">{error}</Note></div>}
        {config && <>
          <div className="space-y-1 break-all text-sm text-ink-muted">
            <p>Resolved environment: <span className="font-mono text-ink">{config.environmentId}</span> · workload: <span className="font-mono text-ink">{config.workload}</span></p>
            <p>Bundle ID: <span className="font-mono text-ink">{config.bundleId}</span></p>
            <p>Immutable bundle digest: <span className="font-mono text-ink">{config.bundleDigest || "not returned by this server"}</span></p>
            <p>Release: <span className="font-mono text-ink">{config.releaseVersion || "not recorded"}</span></p>
          </div>
          {config.frontendConfigUnsupportedReason && <Note tone="neutral">Frontend runtime config unavailable: {config.frontendConfigUnsupportedReason}</Note>}
          <details>
            <summary className="cursor-pointer text-sm font-medium text-ink">Redacted fields and references</summary>
            <dl className="mt-3 space-y-2 text-xs">
              {configRows(config.objects).map((row) => <div key={row.path} className="border-b border-border pb-2"><dt className="break-all font-mono text-ink-muted">{row.path}</dt><dd className="whitespace-pre-wrap break-all font-mono text-ink">{row.value}</dd></div>)}
            </dl>
          </details>
          <div className="flex flex-wrap gap-2">
            <button type="button" className={BUTTON_SECONDARY} onClick={() => void copy()}>Copy redacted YAML</button>
            <button type="button" className={BUTTON_SECONDARY} onClick={download}>Download redacted YAML</button>
          </div>
          {copyState && <p role="status" className="text-sm text-ink-muted">{copyState}</p>}
          <pre aria-label="Server-redacted manifest YAML" className="max-h-96 overflow-auto whitespace-pre rounded-md border border-border bg-surface-sunken p-3 font-mono text-xs text-ink"><code>{config.renderedYaml || "No manifest content returned."}</code></pre>
          <form className="flex flex-wrap items-end gap-2" onSubmit={(event) => { event.preventDefault(); if (release.trim()) void load(release.trim()); }}>
            <label className="space-y-1 text-sm text-ink-muted">Compare with historical release
              <input aria-label="Historical release version" value={release} onChange={(event) => { setRelease(event.target.value); setComparison(null); }} placeholder="v1.2.3" className="block rounded-md border border-border bg-surface px-3 py-2 font-mono text-sm text-ink" />
            </label>
            <button type="submit" className={BUTTON_SECONDARY} disabled={!release.trim() || busy}>Compare redacted values</button>
          </form>
          <p className="text-xs text-ink-muted">Comparison reads both bundles with authorization. Withheld secret values are not compared, hashed or inferred.</p>
          {comparison && <section aria-label="Redacted configuration comparison" className="space-y-3">
            <h3 className="text-sm font-medium text-ink">{comparison.historical.releaseVersion} → {comparison.current.releaseVersion || "selected bundle"}</h3>
            <p className="break-all font-mono text-xs text-ink-muted">Historical bundle: {comparison.historical.bundleId} · {comparison.historical.bundleDigest || "digest not returned"}</p>
            <p className="break-all font-mono text-xs text-ink-muted">Selected bundle: {comparison.current.bundleId} · {comparison.current.bundleDigest || "digest not returned"}</p>
            {!comparison.rows.length && <p className="text-sm text-ink-muted">No visible field changes.</p>}
            {comparison.rows.map((row) => <div key={row.path} className="space-y-1 border-t border-border pt-2 text-xs">
              <p className="break-all font-mono text-ink-muted">{row.path}</p>
              <p className="font-medium text-ink">{row.change}</p>
              <p className="whitespace-pre-wrap break-all font-mono text-ink">Historical: {row.before}</p>
              <p className="whitespace-pre-wrap break-all font-mono text-ink">Selected: {row.after}</p>
            </div>)}
          </section>}
        </>}
      </div>
    </Panel>
  );
}
