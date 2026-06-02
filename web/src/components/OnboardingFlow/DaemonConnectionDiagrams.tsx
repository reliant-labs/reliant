import { useEffect, useId, useRef, useState } from "react";
import {
  Boxes,
  Cloud,
  FolderCode,
  Laptop,
  Monitor,
  Server,
  Sparkles,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";

type Location = {
  id: string;
  icon: LucideIcon;
  name: string;
  sub: string;
};

const LOCATIONS: Location[] = [
  {
    id: "reliant",
    icon: Sparkles,
    name: "Reliant Cloud",
    sub: "Hosted for you",
  },
  { id: "cloud", icon: Cloud, name: "Your cloud", sub: "AWS · GCP · Azure" },
  {
    id: "laptop",
    icon: Laptop,
    name: "Local machine",
    sub: "Laptop or workstation",
  },
  { id: "onprem", icon: Server, name: "On-prem", sub: "Bare metal" },
  { id: "k8s", icon: Boxes, name: "Kubernetes", sub: "Your cluster" },
];

const W = 1060;
const H = 480;
const ROWS = [40, 140, 240, 340, 440];
const DAEMON_X = 556;
const DAEMON_W = 220;
const DAEMON_RIGHT = DAEMON_X + DAEMON_W;
const CLOUD_X = 220;
const CLOUD_W = 210;
const CLOUD_RIGHT = CLOUD_X + CLOUD_W;
const CODE_X = 884;
const CODE_W = 176;
const APP_W = 176;
const HUB_Y = 240;
const CARD_H = 72;
const COMPACT_H = 64;

function curve(x1: number, y1: number, x2: number, y2: number) {
  const mx = (x1 + x2) / 2;
  return `M${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`;
}

function Node({
  icon: Icon,
  title,
  sub,
  highlight,
  compact,
}: {
  icon: LucideIcon;
  title: string;
  sub: string;
  highlight?: boolean;
  compact?: boolean;
}) {
  return (
    <div
      className={cn(
        "flex h-full min-w-0 items-center gap-3 rounded-xl border shadow-sm",
        compact ? "px-3 py-2.5" : "p-3.5",
        highlight
          ? "border-primary/40 bg-primary/10"
          : "border-border/60 bg-background/85",
      )}
    >
      <div
        className={cn(
          "flex shrink-0 items-center justify-center rounded-lg",
          compact ? "h-9 w-9" : "h-10 w-10",
          highlight
            ? "bg-primary text-primary-foreground"
            : "bg-muted text-muted-foreground",
        )}
      >
        <Icon className={compact ? "h-[18px] w-[18px]" : "h-5 w-5"} />
      </div>
      <div className="min-w-0">
        <h3
          className={cn(
            "truncate font-semibold leading-tight text-foreground",
            compact ? "text-sm" : "text-base",
          )}
        >
          {title}
        </h3>
        <p
          className={cn(
            "truncate leading-snug text-muted-foreground",
            compact ? "text-xs" : "text-sm",
          )}
        >
          {sub}
        </p>
      </div>
    </div>
  );
}

export function DaemonConnectionDiagrams({
  className,
}: {
  className?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(1);
  const gradientId = useId();
  const leftGradient = `${gradientId}-left`;
  const rightGradient = `${gradientId}-right`;

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const update = (width: number) => setScale(Math.min(1, width / W));
    update(el.getBoundingClientRect().width);
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width ?? 0;
      update(w);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  return (
    <section
      className={cn(
        "rounded-2xl border border-border/60 bg-card/85 p-5 shadow-sm",
        className,
      )}
    >
      <div className="space-y-5">
        <div className="space-y-2">
          <h2 className="text-lg font-semibold leading-tight text-foreground">
            One control plane. Daemons anywhere.
          </h2>
          <p className="text-m leading-relaxed text-muted-foreground">
            Reliant Cloud routes every task to a daemon beside the code —
            hosted, your cloud, a laptop, on-prem, or a cluster.
          </p>
        </div>

        <div
          ref={containerRef}
          className="mx-auto w-full overflow-hidden"
          style={{ maxWidth: W, aspectRatio: `${W} / ${H}` }}
          aria-hidden
        >
          <div
            className="relative"
            style={{
              width: W,
              height: H,
              transformOrigin: "top left",
              transform: `scale(${scale})`,
            }}
          >
            <svg
              viewBox={`0 0 ${W} ${H}`}
              className="pointer-events-none absolute inset-0 h-full w-full text-muted-foreground"
            >
              <defs>
                <linearGradient
                  id={leftGradient}
                  x1={CLOUD_RIGHT}
                  y1={HUB_Y}
                  x2={DAEMON_X}
                  y2={HUB_Y}
                  gradientUnits="userSpaceOnUse"
                >
                  <stop offset="0" stopColor="currentColor" stopOpacity="0.1" />
                  <stop
                    offset="0.5"
                    stopColor="currentColor"
                    stopOpacity="0.55"
                  />
                  <stop offset="1" stopColor="currentColor" stopOpacity="0.1" />
                </linearGradient>
                <linearGradient
                  id={rightGradient}
                  x1={DAEMON_RIGHT}
                  y1={HUB_Y}
                  x2={CODE_X}
                  y2={HUB_Y}
                  gradientUnits="userSpaceOnUse"
                >
                  <stop offset="0" stopColor="currentColor" stopOpacity="0.1" />
                  <stop
                    offset="0.5"
                    stopColor="currentColor"
                    stopOpacity="0.55"
                  />
                  <stop offset="1" stopColor="currentColor" stopOpacity="0.1" />
                </linearGradient>
              </defs>
              {ROWS.map((y, i) => (
                <g key={i}>
                  <path
                    d={curve(CLOUD_RIGHT, HUB_Y, DAEMON_X, y)}
                    stroke={`url(#${leftGradient})`}
                    strokeWidth="1.6"
                    fill="none"
                  />
                  <path
                    d={curve(DAEMON_RIGHT, y, CODE_X, HUB_Y)}
                    stroke={`url(#${rightGradient})`}
                    strokeWidth="1.6"
                    fill="none"
                  />
                </g>
              ))}
            </svg>

            <div
              className="absolute"
              style={{
                left: 0,
                top: HUB_Y - CARD_H / 2,
                width: APP_W,
                height: CARD_H,
              }}
            >
              <Node icon={Monitor} title="App" sub="Desktop or web" />
            </div>

            <div
              className="absolute"
              style={{
                left: CLOUD_X,
                top: HUB_Y - CARD_H / 2,
                width: CLOUD_W,
                height: CARD_H,
              }}
            >
              <Node
                icon={Sparkles}
                title="Reliant Cloud"
                sub="Routes work"
                highlight
              />
            </div>

            <div
              className="absolute text-xs font-semibold uppercase tracking-[0.18em] text-muted-foreground"
              style={{ left: DAEMON_X, top: ROWS[0] - 28 }}
            ></div>

            {LOCATIONS.map((l, i) => (
              <div
                key={l.id}
                className="absolute"
                style={{
                  left: DAEMON_X,
                  top: ROWS[i] - COMPACT_H / 2,
                  width: DAEMON_W,
                  height: COMPACT_H,
                }}
              >
                <Node icon={l.icon} title={l.name} sub={l.sub} compact />
              </div>
            ))}

            <div
              className="absolute"
              style={{
                left: CODE_X,
                top: HUB_Y - CARD_H / 2,
                width: CODE_W,
                height: CARD_H,
              }}
            >
              <Node
                icon={FolderCode}
                title="Your code"
                sub="Files and commands"
              />
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
