interface Node {
  id: string;
  label: string;
  /** Optional secondary line (e.g. service version, topic name). */
  sub?: string;
}

interface BusBarProps {
  /** Nodes on the left side (typically producers). */
  producers: Node[];
  /** Nodes on the right side (typically consumers). */
  consumers: Node[];
  /** Bus label rendered along the central bar (e.g. "Kafka", "NATS", "EventBridge"). */
  busLabel: string;
  /**
   * Subscription edges (optional). If provided, lines connect specific
   * producers to specific consumers via the bus. Otherwise all producers
   * connect to all consumers (implicit broadcast).
   */
  edges?: Array<{ from: string; to: string }>;
}

/**
 * Horizontal pub/sub bus diagram — producers on the left, consumers on the right,
 * a labeled bus in the middle. Communicates decoupling: producers and consumers
 * don't know about each other, only the bus.
 *
 * Uses a single shared coordinate space (W × H px) so SVG connectors line up with
 * absolute-positioned node cards exactly. The container scales responsively via
 * a parent wrapper if needed; the internal geometry is fixed.
 */
export default function BusBar({ producers, consumers, busLabel, edges }: BusBarProps) {
  // single coordinate space (px) shared by the SVG viewBox and the cards
  const W = 880;
  const NODE_W = 180;
  const NODE_H = 56;
  const ROW_GAP = 18;
  const PROD_LEFT = 24;
  const CONS_RIGHT = W - 24;
  const CONS_LEFT = CONS_RIGHT - NODE_W;
  const BUS_X1 = PROD_LEFT + NODE_W + 32;
  const BUS_X2 = CONS_LEFT - 32;
  const BUS_CENTER_X = (BUS_X1 + BUS_X2) / 2;
  const BUS_WIDTH = BUS_X2 - BUS_X1;

  const rows = Math.max(producers.length, consumers.length);
  const H = 80 + rows * (NODE_H + ROW_GAP);
  const STACK_TOP = 60;

  const prodCenter = (i: number) => STACK_TOP + i * (NODE_H + ROW_GAP) + NODE_H / 2;
  const consCenter = (i: number) => STACK_TOP + i * (NODE_H + ROW_GAP) + NODE_H / 2;

  const prodById = new Map(producers.map((n, i) => [n.id, prodCenter(i)]));
  const consById = new Map(consumers.map((n, i) => [n.id, consCenter(i)]));

  const renderedEdges =
    edges ?? producers.flatMap((p) => consumers.map((c) => ({ from: p.id, to: c.id })));

  return (
    <div style={{ position: "relative", width: W, height: H }} className="mx-auto text-gray-900">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="absolute inset-0 h-full w-full"
        style={{ pointerEvents: "none" }}
      >
        <defs>
          <marker id="bb-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
            <polygon points="0 0, 8 4, 0 8" fill="#94a3b8" />
          </marker>
        </defs>

        {/* central bus */}
        <rect
          x={BUS_X1}
          y={STACK_TOP - 8}
          width={BUS_WIDTH}
          height={H - STACK_TOP - 16}
          rx="6"
          fill="#1e293b"
        />
        <text
          x={BUS_CENTER_X}
          y={STACK_TOP - 16}
          textAnchor="middle"
          className="fill-gray-700"
          fontSize="11"
          fontWeight="600"
          style={{ textTransform: "uppercase", letterSpacing: "0.12em" }}
        >
          {busLabel}
        </text>

        {/* edges */}
        {renderedEdges.map((e, i) => {
          const y1 = prodById.get(e.from);
          const y2 = consById.get(e.to);
          if (y1 == null || y2 == null) return null;
          return (
            <g key={i}>
              <line
                x1={PROD_LEFT + NODE_W}
                y1={y1}
                x2={BUS_X1}
                y2={y1}
                stroke="#94a3b8"
                strokeWidth="1.5"
              />
              <line
                x1={BUS_X2}
                y1={y2}
                x2={CONS_LEFT}
                y2={y2}
                stroke="#94a3b8"
                strokeWidth="1.5"
                markerEnd="url(#bb-arrow)"
              />
            </g>
          );
        })}
      </svg>

      {producers.map((n, i) => (
        <div
          key={n.id}
          style={{
            position: "absolute",
            left: PROD_LEFT,
            top: STACK_TOP + i * (NODE_H + ROW_GAP),
            width: NODE_W,
            height: NODE_H,
          }}
          className="flex flex-col justify-center rounded-md border border-gray-200 bg-white px-3"
        >
          <div className="truncate text-sm font-semibold">{n.label}</div>
          {n.sub && <div className="truncate text-xs text-gray-500">{n.sub}</div>}
        </div>
      ))}

      {consumers.map((n, i) => (
        <div
          key={n.id}
          style={{
            position: "absolute",
            left: CONS_LEFT,
            top: STACK_TOP + i * (NODE_H + ROW_GAP),
            width: NODE_W,
            height: NODE_H,
          }}
          className="flex flex-col justify-center rounded-md border border-gray-200 bg-white px-3"
        >
          <div className="truncate text-sm font-semibold">{n.label}</div>
          {n.sub && <div className="truncate text-xs text-gray-500">{n.sub}</div>}
        </div>
      ))}
    </div>
  );
}
