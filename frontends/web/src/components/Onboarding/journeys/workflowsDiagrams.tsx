/**
 * Still diagrams of each idea the workflows journey explains, drawn in the
 * app's theme colours. The phone deck (workflowsPhone.tsx) shows them in
 * place of the canvas; nothing in them moves.
 */

import type { ReactNode } from "react";

const W = 280;
const H = 120;

const C = {
  fg: "hsl(var(--foreground))",
  muted: "hsl(var(--muted-foreground))",
  border: "hsl(var(--border))",
  card: "hsl(var(--card))",
  primary: "hsl(var(--primary))",
  agent: "#8b5cf6",
  loop: "#a855f7",
  switch: "#0ea5e9",
  approval: "#f59e0b",
  trigger: "#10b981",
};

function Frame({ label, children }: { label: string; children: ReactNode }) {
  return (
    <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={label} className="h-auto w-full" data-testid="wf-diagram">
      <defs>
        <marker id="wf-d-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" fill={C.muted} />
        </marker>
        <marker id="wf-d-arrow-p" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">
          <path d="M 0 0 L 10 5 L 0 10 z" fill={C.primary} />
        </marker>
      </defs>
      {children}
    </svg>
  );
}

function Node({ x, y, w = 84, h = 34, kind, name, color }: { x: number; y: number; w?: number; h?: number; kind: string; name: string; color: string }) {
  return (
    <g>
      <rect x={x} y={y} width={w} height={h} rx={7} fill={C.card} stroke={color} strokeWidth={1.5} />
      <text x={x + 8} y={y + 13} fontSize={7.5} fontWeight={700} fill={color} letterSpacing={0.6}>
        {kind}
      </text>
      <text x={x + 8} y={y + 26} fontSize={10} fill={C.fg}>
        {name}
      </text>
    </g>
  );
}

const Dot = ({ x, y, color = C.muted }: { x: number; y: number; color?: string }) => <circle cx={x} cy={y} r={3.5} fill={color} />;

const Line = ({ d, primary, dashed }: { d: string; primary?: boolean; dashed?: boolean }) => (
  <path
    d={d}
    fill="none"
    stroke={primary ? C.primary : C.muted}
    strokeWidth={primary ? 2 : 1.4}
    strokeDasharray={dashed ? "5 4" : undefined}
    markerEnd={`url(#${primary ? "wf-d-arrow-p" : "wf-d-arrow"})`}
  />
);

const Note = ({ x, y, children, anchor = "start", color = C.muted }: { x: number; y: number; children: ReactNode; anchor?: "start" | "middle" | "end"; color?: string }) => (
  <text x={x} y={y} fontSize={9} fill={color} textAnchor={anchor}>
    {children}
  </text>
);

export function StepDiagram() {
  return (
    <Frame label="An agent step called review, with its prompt">
      <Node x={70} y={20} w={140} h={80} kind="AGENT" name="review" color={C.agent} />
      <rect x={78} y={56} width={124} height={8} rx={3} fill={C.border} />
      <rect x={78} y={70} width={100} height={8} rx={3} fill={C.border} />
      <rect x={78} y={84} width={112} height={8} rx={3} fill={C.border} />
      <Dot x={70} y={60} />
      <Dot x={210} y={60} color={C.agent} />
      <Note x={222} y={50}>its prompt</Note>
    </Frame>
  );
}

const CURSOR = "M0 0 L0 15 L4 11 L7 18 L10 17 L7 10 L12 10 Z";

export function EdgeDiagram() {
  return (
    <Frame label="Drag from the dot on the right of review to the dot on the left of fix_until_green to connect them">
      <Node x={12} y={20} kind="AGENT" name="review" color={C.agent} />
      <Node x={184} y={66} kind="LOOP" name="fix_until_green" color={C.loop} />
      <Dot x={96} y={37} color={C.primary} />
      <circle cx={96} cy={37} r={8} fill="none" stroke={C.primary} strokeWidth={1.5} />
      <Dot x={184} y={83} />
      <Line d="M 96 37 C 140 37 140 83 180 83" primary dashed />
      <g transform="translate(136 58)">
        <path d={CURSOR} fill={C.fg} stroke={C.card} strokeWidth={1} />
      </g>
      <Note x={108} y={24} color={C.primary}>
        drag dot to dot
      </Note>
    </Frame>
  );
}

export function LoopDiagram() {
  return (
    <Frame label="A loop: fix, then test, repeated while the tests fail, up to 3 times">
      <rect x={10} y={14} width={190} height={92} rx={9} fill="none" stroke={C.loop} strokeWidth={1.5} strokeDasharray="4 3" />
      <Note x={18} y={28} color={C.loop}>
        LOOP fix_until_green
      </Note>
      <Node x={22} y={40} w={70} kind="AGENT" name="fix" color={C.agent} />
      <Node x={118} y={40} w={70} kind="RUN" name="test" color={C.muted} />
      <Line d="M 92 57 L 114 57" />
      <Line d="M 153 74 C 153 98 57 98 57 78" />
      <Note x={105} y={101} anchor="middle">
        again while tests fail, up to 3
      </Note>
      <Line d="M 200 57 L 262 57" primary />
      <Note x={232} y={48} anchor="middle" color={C.primary}>
        tests pass
      </Note>
    </Frame>
  );
}

export function BranchDiagram() {
  return (
    <Frame label="A switch: if the tests pass, an approval asks you before merge; otherwise report">
      <Node x={6} y={42} w={62} kind="SWITCH" name="tests?" color={C.switch} />
      <Node x={98} y={8} w={86} kind="APPROVAL" name="ask you" color={C.approval} />
      <Node x={206} y={8} w={68} kind="AGENT" name="merge" color={C.agent} />
      <Node x={98} y={76} w={86} kind="AGENT" name="report" color={C.agent} />
      <Line d="M 68 52 C 82 52 82 25 94 25" />
      <Line d="M 68 66 C 82 66 82 93 94 93" />
      <Line d="M 184 25 L 202 25" primary />
      <Note x={66} y={34} anchor="middle">pass</Note>
      <Note x={50} y={104} anchor="middle">otherwise</Note>
      <Note x={193} y={56} anchor="middle" color={C.approval}>
        you approve
      </Note>
    </Frame>
  );
}

export function TriggerDiagram() {
  return (
    <Frame label="A schedule, weekdays at 9:00, starts the workflow once it is turned on">
      <rect x={10} y={30} width={120} height={60} rx={8} fill={C.card} stroke={C.trigger} strokeWidth={1.5} />
      <circle cx={32} cy={52} r={11} fill="none" stroke={C.trigger} strokeWidth={1.5} />
      <path d="M 32 45 L 32 52 L 37 55" stroke={C.trigger} strokeWidth={1.5} fill="none" />
      <Note x={50} y={50} color={C.fg}>
        Weekdays
      </Note>
      <Note x={50} y={62} color={C.fg}>
        at 9:00
      </Note>
      <rect x={20} y={70} width={44} height={14} rx={7} fill="none" stroke={C.border} />
      <circle cx={28} cy={77} r={4} fill={C.muted} />
      <Note x={38} y={80}>off</Note>
      <Line d="M 130 60 L 172 60" dashed />
      <Node x={176} y={43} w={96} kind="WORKFLOW" name="Fix my PR" color={C.primary} />
      <Note x={151} y={52} anchor="middle">starts</Note>
    </Frame>
  );
}

export function MachineDiagram() {
  return (
    <Frame label="Runs on: each run happens on a machine you choose, or no machine">
      <Node x={8} y={43} w={92} kind="WORKFLOW" name="Fix my PR" color={C.primary} />
      <Line d="M 100 60 L 140 60" />
      <Note x={120} y={52} anchor="middle">runs on</Note>
      <rect x={144} y={18} width={128} height={84} rx={8} fill={C.card} stroke={C.border} />
      <rect x={154} y={30} width={108} height={20} rx={5} fill="none" stroke={C.primary} strokeWidth={1.5} />
      <Note x={160} y={44} color={C.fg}>
        cloud machine
      </Note>
      <path d="M 252 37 L 256 42 L 260 37" stroke={C.muted} fill="none" />
      <Note x={156} y={68}>your computer</Note>
      <Note x={156} y={84}>No machine</Note>
      <Note x={156} y={96}>(web and integrations)</Note>
    </Frame>
  );
}

export function InputsDiagram() {
  return (
    <Frame label="The workflow's inputs, pr and test_command, become controls under the chat's message box">
      <rect x={10} y={16} width={150} height={88} rx={8} fill={C.card} stroke={C.border} />
      <Note x={20} y={32} color={C.fg}>
        Message box
      </Note>
      <rect x={20} y={44} width={58} height={18} rx={9} fill="none" stroke={C.primary} />
      <Note x={30} y={56} color={C.primary}>pr</Note>
      <rect x={84} y={44} width={68} height={18} rx={9} fill="none" stroke={C.primary} />
      <Note x={90} y={56} color={C.primary}>test_command</Note>
      <rect x={20} y={72} width={130} height={22} rx={5} fill="none" stroke={C.border} />
      <Line d="M 200 60 L 164 60" />
      <Node x={204} y={43} w={70} kind="WORKFLOW" name="inputs" color={C.primary} />
    </Frame>
  );
}
