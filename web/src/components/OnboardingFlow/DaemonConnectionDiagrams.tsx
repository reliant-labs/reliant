import { Cloud, FolderCode, Monitor, Server } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";

type DiagramNode = {
  label: string;
  detail: string;
  icon: LucideIcon;
  tone?: "default" | "primary";
};

const clientNode: DiagramNode = {
  label: "App",
  detail: "Desktop or web",
  icon: Monitor,
};

const cloudNode: DiagramNode = {
  label: "Reliant Cloud",
  detail: "Routes work",
  icon: Cloud,
  tone: "primary",
};

const daemonNode: DiagramNode = {
  label: "Daemon",
  detail: "Hosted or self-run",
  icon: Server,
};

const codeNode: DiagramNode = {
  label: "Your code",
  detail: "Files and commands",
  icon: FolderCode,
};

function NodeCard({ node }: { node: DiagramNode }) {
  const Icon = node.icon;
  const isPrimary = node.tone === "primary";

  return (
    <div
      className={cn(
        "min-w-0 rounded-xl border p-3 shadow-sm",
        isPrimary
          ? "border-primary/30 bg-primary/10"
          : "border-border/60 bg-background/85",
      )}
    >
      <div className="flex min-w-0 items-start gap-2.5">
        <div
          className={cn(
            "flex h-8 w-8 shrink-0 items-center justify-center rounded-lg",
            isPrimary ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground",
          )}
        >
          <Icon className="h-4 w-4" />
        </div>
        <div className="min-w-0 space-y-0.5">
          <h3 className="break-words text-sm font-semibold leading-tight text-foreground">
            {node.label}
          </h3>
          <p className="break-words text-xs leading-snug text-muted-foreground">
            {node.detail}
          </p>
        </div>
      </div>
    </div>
  );
}

function Connector({ label, emphasis = false }: { label: string; emphasis?: boolean }) {
  return (
    <div className="flex min-h-7 min-w-0 items-center justify-center gap-2 text-[10px] text-muted-foreground md:flex-col md:gap-1 md:py-2">
      <div
        className={cn(
          "h-5 w-px shrink-0 rounded-full md:h-px md:w-full",
          emphasis ? "bg-primary/70" : "bg-border",
        )}
      />
      <span
        className={cn(
          "max-w-full rounded-full border px-1.5 py-0.5 font-medium leading-none",
          emphasis
            ? "border-primary/25 bg-primary/10 text-primary"
            : "border-border/50 bg-background/70",
        )}
      >
        {label}
      </span>
    </div>
  );
}

export function DaemonConnectionDiagrams({ className }: { className?: string }) {
  return (
    <section
      className={cn(
        "rounded-2xl border border-border/60 bg-card/85 p-4 shadow-sm",
        className,
      )}
    >
      <div className="space-y-4">
        <div className="space-y-1.5">
          <p className="text-[11px] font-medium uppercase tracking-[0.18em] text-primary">
            Smart routing
          </p>
          <h2 className="text-base font-semibold leading-tight text-foreground">
            Reliant routes each task to the daemon beside the code.
          </h2>
          <p className="text-xs leading-relaxed text-muted-foreground">
            Pick hosted or self-run compute. Either way, the daemon is the only piece that needs filesystem access.
          </p>
        </div>

        <div className="grid min-w-0 gap-2 md:grid-cols-[minmax(0,1fr)_2rem_minmax(0,1fr)_2rem_minmax(0,1fr)_2rem_minmax(0,1fr)] md:items-stretch">
          <NodeCard node={clientNode} />
          <Connector label="start" />
          <NodeCard node={cloudNode} />
          <Connector label="route" emphasis />
          <NodeCard node={daemonNode} />
          <Connector label="access" />
          <NodeCard node={codeNode} />
        </div>
      </div>
    </section>
  );
}
