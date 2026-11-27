// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Strategy: what the org is going for and how it will know, above the
 * campaigns (every campaign belongs to one strategy), and the strategy
 * library to start a new one from. Views on the URL: the org's strategies
 * (default), one strategy (`?strategy=<id>`), and the library
 * (`?view=library`, one entry open over it with `&entry=<key>`).
 */

import { useSearch } from "@tanstack/react-router";
import { BookOpen } from "lucide-react";

import Button from "@/components/forge-ui/button";
import PageHeader from "@/components/forge-ui/page_header";
import Tabs from "@/components/forge-ui/tabs";

import { GtmShell } from "../shared/GtmShell";
import { LibraryView } from "./Library";
import { useStrategyNav, type StrategyPageView } from "./nav";
import { StrategiesView, StrategyDetail } from "./Strategies";

export function StrategyPage() {
  return (
    <GtmShell page="strategy">
      <StrategyView />
    </GtmShell>
  );
}

export function StrategyView() {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const nav = useStrategyNav();
  const view: StrategyPageView = search.view === "library" ? "library" : "strategies";
  const strategyId = typeof search.strategy === "string" ? search.strategy : "";
  const entryKey = typeof search.entry === "string" ? search.entry : "";

  return (
    <>
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
        <PageHeader title="Strategy" meta={<span>What you are going for, how you will know, and the campaigns that get you there.</span>} className="" />
        {view === "strategies" && !strategyId && (
          <Button variant="outline" size="sm" leftIcon={<BookOpen />} onClick={nav.library}>
            Browse the library
          </Button>
        )}
      </div>
      <Tabs
        tabs={[
          { id: "strategies", label: "Your strategies" },
          { id: "library", label: "Library" },
        ]}
        activeTab={view}
        onChange={(id) => (id === "library" ? nav.library() : nav.list())}
        ariaLabel="Strategy view"
      />
      {view === "library" ? (
        <LibraryView onStarted={nav.strategy} entryKey={entryKey} onOpenEntry={nav.entry} onCloseEntry={nav.library} />
      ) : strategyId ? <StrategyDetail id={strategyId} /> : <StrategiesView />}
    </>
  );
}
