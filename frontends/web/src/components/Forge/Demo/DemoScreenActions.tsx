// Copyright (c) 2025 Reliant Labs

/** Persistent screen actions, separate from page explanations and data/header controls. */
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "@tanstack/react-router";
import { trackEvent } from "@/lib/analytics";
import { SignUpButton, InstallForgeLink } from "./DemoCta";
import { useConsoleDrawerOpen } from "./DemoCorner";
import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "./demoPath";

export function DemoScreenActions() {
  const [host, setHost] = useState<HTMLElement | null>(null);
  const drawerOpen = useConsoleDrawerOpen();
  const navigate = useNavigate();
  useEffect(() => {
    const slot = document.createElement("div");
    slot.className = "w-full shrink-0";
    const attach = () => {
      const main = document.querySelector("main");
      // A dedicated row below the existing header, outside its controls and
      // above the retained scroll host. Never overlay data or the mobile drawer.
      if (main?.parentElement && slot.parentElement !== main.parentElement) {
        main.parentElement.insertBefore(slot, main);
        setHost(slot);
      }
    };
    attach(); const observer = new MutationObserver(attach);
    observer.observe(document.body, {childList:true,subtree:true});
    return () => { observer.disconnect();slot.remove(); };
  }, []);
  if (!host || drawerOpen) return null;
  return createPortal(
    <nav aria-label="Demo actions" data-testid="demo-screen-actions" className="relative z-[101] flex min-h-[52px] flex-wrap items-center justify-end gap-x-3 gap-y-1 border-b border-border bg-surface px-3 py-1 text-xs">
      <a href={`${FORGE_DEMO_PATH}?section=${DEMO_SECTION_BUILT}`} className="touch-target mr-auto inline-flex items-center font-medium text-primary-ink underline-offset-2 hover:underline sm:mr-0" onClick={(event) => {
        if(event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
        event.preventDefault(); trackEvent("demo_how_built_opened",{from:"banner"});
        void navigate({to:FORGE_DEMO_PATH,search:{section:DEMO_SECTION_BUILT,tour:undefined}} as never);
      }}>How Forge builds this</a>
      <InstallForgeLink placement="banner">Install Forge</InstallForgeLink>
      <SignUpButton placement="banner" size="md" />
    </nav>,host,
  );
}
