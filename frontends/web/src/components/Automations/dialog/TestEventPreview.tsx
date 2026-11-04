// Copyright (c) 2025 Reliant Labs

import { useEffect, useId, useRef, useState } from "react";
import { create, type JsonObject } from "@bufbuild/protobuf";
import { ReplayEventSchema } from "@/gen/reliant/v1/chat_pb";
import { PreviewReplayRequestSchema } from "@/gen/reliant/v1/trigger_pb";
import { triggerServiceQueries } from "@/hooks/trigger-service-hooks_gen";
import { useQueryClient } from "@tanstack/react-query";
import type { Trigger } from "@/hooks/trigger-model";
import Button from "@/components/forge-ui/button";
import { textareaClass, hintClass, labelClass } from "../../workflow/run/runFormStyles";

/** Authenticated mapping only; never POSTs to a webhook or calls FireTrigger. */
export function TestEventPreview({ trigger }: { trigger: Trigger }) {
  const id = useId();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [sample, setSample] = useState(trigger.webhookUrl || (trigger.source.kind === "passthrough" && trigger.source.arm.case === "webhook") ? '{"body":{"hello":"world"}}' : '{"data":{}}');
  const [error, setError] = useState("");
  const [result, setResult] = useState<string>();
  const [pending, setPending] = useState(false);
  const intent = useRef(0);
  useEffect(() => () => { intent.current += 1; }, [trigger.id]);
  const send = async () => {
    // Edits and later previews own the UI even if an older RPC completes last.
    const current = ++intent.current;
    setError("");
    setResult(undefined);
    let payload: JsonObject;
    try {
      const parsed: unknown = JSON.parse(sample);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("object required");
      payload = parsed as JsonObject;
    } catch {
      setError("Enter a JSON object for the sample event.");
      return;
    }
    setPending(true);
    try {
      const mapped = await queryClient.fetchQuery({ ...triggerServiceQueries.previewReplay(create(PreviewReplayRequestSchema, { triggerId: trigger.id, replay: create(ReplayEventSchema, { payload, occurredAt: new Date().toISOString() }) })), gcTime: 0, retry: false });
      if (intent.current === current) setResult(mapped.prompt);
    } catch {
      // Mapping errors can contain user-provided values. Never echo raw RPC
      // errors or log a payload, token, URL or headers from an event sample.
      if (intent.current === current) setError("Could not preview this sample. Check its fields and try again.");
    } finally { if (intent.current === current) setPending(false); }
  };
  return (
    <div className="space-y-2">
      <Button type="button" variant="outline" size="sm" onClick={() => setOpen((v) => !v)} aria-expanded={open} aria-controls={id}>Send a test event</Button>
      {open && <div id={id} className="space-y-2 rounded-md border border-border/60 bg-background p-3">
        <p className={hintClass}>Read-only preview: no workflow runs, no external writes, and nothing is enabled or recorded. This tests the event-to-message mapping, not webhook delivery, authentication or filters. Do not include secrets.</p>
        <label htmlFor={`${id}-sample`} className={labelClass}>Sample event (JSON)</label>
        <textarea id={`${id}-sample`} rows={4} className={textareaClass} value={sample} onChange={(e) => { intent.current += 1; setPending(false); setSample(e.target.value); setError(""); setResult(undefined); }} aria-invalid={!!error} aria-describedby={error ? `${id}-error` : undefined} />
        <Button type="button" size="sm" variant="primary" onClick={() => void send()} loading={pending}>Preview message</Button>
        {error && <p id={`${id}-error`} role="alert" className="text-sm text-destructive">{error}</p>}
        {result !== undefined && <div role="status"><p className={labelClass}>Message the run would receive</p><pre className="whitespace-pre-wrap break-words text-xs text-foreground">{result}</pre></div>}
      </div>}
    </div>
  );
}
