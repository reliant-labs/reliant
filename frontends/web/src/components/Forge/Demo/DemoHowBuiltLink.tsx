// Copyright (c) 2025 Reliant Labs
import { lazy, Suspense, useRef, useState } from "react";
import { Hammer } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { trackEvent } from "@/lib/analytics";
import { DemoDialog } from "./DemoDialog";

const HowThisAppWasBuilt = lazy(() => import("./howBuilt/HowThisAppWasBuilt"));

/** Same contextual action in the persistent row, welcome and every page card. */
export function DemoHowBuiltLink({ placement, onOpen }: { placement: "banner" | "welcome" | "page_explanation"; onOpen?: () => void }) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  return <>
    <Button ref={trigger} variant="outline" className="min-h-[44px] text-sm" leftIcon={<Hammer />} onClick={() => {
      trackEvent("demo_how_built_opened", { from: placement });
      if (onOpen) onOpen();
      else setOpen(true);
    }}>How Forge builds this</Button>
    <DemoDialog open={open} onClose={() => setOpen(false)} fallbackFocus={() => trigger.current?.focus()} size="xl" title="How Forge builds this" footer={<Button variant="outline" className="min-h-[44px]" onClick={() => setOpen(false)}>Back to exploring</Button>}>
      <Suspense fallback={<p role="status">Loading the recorded build…</p>}><HowThisAppWasBuilt autoPlay={false} /></Suspense>
    </DemoDialog>
  </>;
}
