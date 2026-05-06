import { memo, useMemo } from 'react';
import { createRoot } from 'react-dom/client';
import {
  Background,
  BaseEdge,
  EdgeLabelRenderer,
  getSmoothStepPath,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import './styles.css';

const agentStreams = [
  {
    id: 'agent-b',
    name: 'Agent B',
    accent: 'cyan',
    title: 'Implementation Loop',
    output: 'Feature patch',
    steps: ['Draft change', 'Run checks', 'Patch feedback'],
    evidence: 'Diff, build log, assumptions',
  },
  {
    id: 'agent-c',
    name: 'Agent C',
    accent: 'violet',
    title: 'Research Loop',
    output: 'Context pack',
    steps: ['Trace code', 'Compare paths', 'Summarize risks'],
    evidence: 'References, edge cases, alternatives',
  },
  {
    id: 'agent-d',
    name: 'Agent D',
    accent: 'amber',
    title: 'Test Loop',
    output: 'Verification matrix',
    steps: ['Write cases', 'Reproduce issue', 'Tighten assertions'],
    evidence: 'Pass/fail table, retry notes',
  },
];

const retrySteps = [
  'Attempt 1 applies reviewer feedback',
  'Attempt 2 narrows the failing branch',
  'Attempt 3 validates the corrected output',
];

const capabilityPillars = [
  'Planning',
  'Parallel outputs',
  'Multiple agents',
  'Custom loops',
  'Structured review',
  'Retry feedback',
  'Rethink recovery',
];

const edgeTones = {
  primary: '#5ee7ff',
  pass: '#7dffa0',
  fail: '#ff6685',
  rethink: '#ffcf5a',
  neutral: '#9fb2ff',
};

const nodeTypes = {
  planner: memo(PlannerNode),
  fanout: memo(FanoutNode),
  agent: memo(AgentNode),
  review: memo(ReviewNode),
  retry: memo(RetryNode),
  rethink: memo(RethinkNode),
  final: memo(FinalNode),
};

const edgeTypes = {
  labeled: LabeledEdge,
};

function FlowHandles() {
  return (
    <>
      <Handle type="target" id="top-target" position={Position.Top} className="flow-handle" />
      <Handle type="source" id="top-source" position={Position.Top} className="flow-handle" />
      <Handle type="target" id="right-target" position={Position.Right} className="flow-handle" />
      <Handle type="source" id="right-source" position={Position.Right} className="flow-handle" />
      <Handle type="target" id="bottom-target" position={Position.Bottom} className="flow-handle" />
      <Handle type="source" id="bottom-source" position={Position.Bottom} className="flow-handle" />
      <Handle type="target" id="left-target" position={Position.Left} className="flow-handle" />
      <Handle type="source" id="left-source" position={Position.Left} className="flow-handle" />
    </>
  );
}

function SignalBadge({ children, tone = 'neutral' }) {
  return <span className={`signal-badge ${tone}`}>{children}</span>;
}

function NodeShell({ children, tone, className = '', ariaLabel }) {
  return (
    <article className={`workflow-node ${tone} ${className}`} aria-label={ariaLabel}>
      <FlowHandles />
      {children}
    </article>
  );
}

function PlannerNode({ data }) {
  return (
    <NodeShell tone="planner" className="planner-node" ariaLabel="Agent A planning node">
      <div className="node-topline">
        <span className="node-monogram">A</span>
        <SignalBadge tone="planner">Agent A plans</SignalBadge>
      </div>
      <h2>{data.title}</h2>
      <p>{data.description}</p>
      <div className="node-grid compact">
        {data.points.map((point) => (
          <span key={point}>{point}</span>
        ))}
      </div>
    </NodeShell>
  );
}

function FanoutNode({ data }) {
  return (
    <NodeShell tone="parallel" className="fanout-node" ariaLabel="Parallelizable outputs fan out node">
      <div className="fanout-orb" />
      <div>
        <SignalBadge tone="parallel">Parallelizable outputs</SignalBadge>
        <h2>{data.title}</h2>
        <p>{data.description}</p>
      </div>
    </NodeShell>
  );
}

function AgentNode({ data }) {
  return (
    <NodeShell tone={data.accent} className="agent-node" ariaLabel={`${data.name} ${data.title}`}>
      <div className="agent-node__header">
        <SignalBadge tone={data.accent}>{data.name}</SignalBadge>
        <span>custom loop</span>
      </div>
      <h3>{data.title}</h3>
      <ol className="loop-steps">
        {data.steps.map((step) => (
          <li key={step}>{step}</li>
        ))}
      </ol>
      <div className="agent-node__evidence">
        <strong>{data.output}</strong>
        <span>{data.evidence}</span>
      </div>
    </NodeShell>
  );
}

function ReviewNode({ data }) {
  return (
    <NodeShell tone="review" className="review-node" ariaLabel="Structured review decision hub">
      <div className="review-core">
        <SignalBadge tone="review">Decision hub</SignalBadge>
        <h2>{data.title}</h2>
        <p>{data.description}</p>
      </div>
      <div className="decision-grid">
        <div className="decision-pill pass">
          <span>Pass</span>
          <strong>Ready for synthesis</strong>
        </div>
        <div className="decision-pill fail">
          <span>Fail</span>
          <strong>Feedback required</strong>
        </div>
      </div>
    </NodeShell>
  );
}

function RetryNode() {
  return (
    <NodeShell tone="fail" className="retry-node" ariaLabel="Retry path with reviewer feedback">
      <SignalBadge tone="fail">Fail path</SignalBadge>
      <h2>Retry with feedback</h2>
      <ul>
        {retrySteps.map((step) => (
          <li key={step}>{step}</li>
        ))}
      </ul>
      <div className="retry-limit">
        <span>Hard stop</span>
        <strong>Retries exceed 3</strong>
      </div>
    </NodeShell>
  );
}

function RethinkNode() {
  return (
    <NodeShell tone="rethink" className="rethink-node" ariaLabel="Rethink path loops back to planning">
      <SignalBadge tone="rethink">Rethink path</SignalBadge>
      <h2>Change the plan</h2>
      <p>Adjust decomposition, constraints, or agent assignments before relaunching the pipeline.</p>
    </NodeShell>
  );
}

function FinalNode() {
  return (
    <NodeShell tone="success" className="final-node" ariaLabel="Final answer node">
      <div className="node-topline">
        <span className="node-monogram final">OK</span>
        <SignalBadge tone="success">Final answer</SignalBadge>
      </div>
      <h2>Synthesize verified work</h2>
      <p>Deliver reviewed outputs, decisions, risks, and next steps instead of a single unverified guess.</p>
      <div className="final-stack">
        <span>Verified outputs</span>
        <span>Review rationale</span>
        <span>Next actions</span>
      </div>
    </NodeShell>
  );
}

function LabeledEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
  style,
  data,
}) {
  const [edgePath, labelX, labelY] = getSmoothStepPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
    borderRadius: 22,
    offset: data?.offset ?? 28,
  });

  return (
    <>
      <BaseEdge id={id} path={edgePath} markerEnd={markerEnd} style={style} />
      {data?.label ? (
        <EdgeLabelRenderer>
          <div
            className={`edge-label ${data.tone ?? 'primary'}`}
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
          >
            {data.label}
          </div>
        </EdgeLabelRenderer>
      ) : null}
    </>
  );
}

function buildEdge({ id, source, target, sourceHandle, targetHandle, label, tone = 'primary', animated = false, offset }) {
  const color = edgeTones[tone] ?? edgeTones.primary;

  return {
    id,
    source,
    target,
    sourceHandle,
    targetHandle,
    type: 'labeled',
    focusable: false,
    selectable: false,
    animated,
    data: { label, tone, offset },
    markerEnd: { type: MarkerType.ArrowClosed, color, width: 18, height: 18 },
    style: {
      stroke: color,
      strokeWidth: tone === 'neutral' ? 1.7 : 2.2,
      strokeDasharray: tone === 'fail' || tone === 'rethink' ? '8 8' : undefined,
      filter: `drop-shadow(0 0 8px ${color}55)`,
    },
  };
}

function App() {
  const nodes = useMemo(
    () => [
      {
        id: 'plan',
        type: 'planner',
        position: { x: 470, y: 18 },
        focusable: false,
        selectable: false,
        data: {
          title: 'Decompose the chat',
          description:
            'Turn a broad request into milestones, dependencies, acceptance criteria, and safe parallel work.',
          points: ['scope', 'dependencies', 'criteria', 'parallel lanes'],
        },
      },
      {
        id: 'rethink',
        type: 'rethink',
        position: { x: 50, y: 96 },
        focusable: false,
        selectable: false,
        data: {},
      },
      {
        id: 'fanout',
        type: 'fanout',
        position: { x: 460, y: 242 },
        focusable: false,
        selectable: false,
        data: {
          title: 'Emit work packets',
          description: 'Each packet carries context, success criteria, and the review contract for one branch.',
        },
      },
      ...agentStreams.map((stream, index) => ({
        id: stream.id,
        type: 'agent',
        position: { x: 36 + index * 424, y: 414 },
        focusable: false,
        selectable: false,
        data: stream,
      })),
      {
        id: 'retry',
        type: 'retry',
        position: { x: 44, y: 706 },
        focusable: false,
        selectable: false,
        data: {},
      },
      {
        id: 'review',
        type: 'review',
        position: { x: 456, y: 696 },
        focusable: false,
        selectable: false,
        data: {
          title: 'Structured review returns pass or fail',
          description:
            'Reviewer checks evidence quality, acceptance criteria, regressions, and readiness for synthesis.',
        },
      },
      {
        id: 'final',
        type: 'final',
        position: { x: 886, y: 706 },
        focusable: false,
        selectable: false,
        data: {},
      },
    ],
    [],
  );

  const edges = useMemo(
    () => [
      buildEdge({
        id: 'plan-to-fanout',
        source: 'plan',
        sourceHandle: 'bottom-source',
        target: 'fanout',
        targetHandle: 'top-target',
        label: 'emits parallelizable work',
      }),
      ...agentStreams.map((stream) =>
        buildEdge({
          id: `fanout-to-${stream.id}`,
          source: 'fanout',
          sourceHandle: 'bottom-source',
          target: stream.id,
          targetHandle: 'top-target',
          label: stream.id === 'agent-c' ? 'parallel launch' : undefined,
          tone: 'neutral',
        }),
      ),
      ...agentStreams.map((stream) =>
        buildEdge({
          id: `${stream.id}-to-review`,
          source: stream.id,
          sourceHandle: 'bottom-source',
          target: 'review',
          targetHandle: 'top-target',
          label: stream.id === 'agent-c' ? 'submit evidence' : undefined,
          tone: 'neutral',
        }),
      ),
      buildEdge({
        id: 'review-to-final',
        source: 'review',
        sourceHandle: 'right-source',
        target: 'final',
        targetHandle: 'left-target',
        label: 'pass',
        tone: 'pass',
        offset: 38,
      }),
      buildEdge({
        id: 'review-to-retry',
        source: 'review',
        sourceHandle: 'left-source',
        target: 'retry',
        targetHandle: 'right-target',
        label: 'fail with reasons',
        tone: 'fail',
        animated: true,
        offset: 42,
      }),
      buildEdge({
        id: 'retry-to-review',
        source: 'retry',
        sourceHandle: 'bottom-source',
        target: 'review',
        targetHandle: 'bottom-target',
        label: 'resubmit corrected output',
        tone: 'fail',
        offset: 52,
      }),
      buildEdge({
        id: 'retry-to-agent-b',
        source: 'retry',
        sourceHandle: 'top-source',
        target: 'agent-b',
        targetHandle: 'left-target',
        label: 'targeted feedback to failing loop',
        tone: 'fail',
        animated: true,
        offset: 34,
      }),
      buildEdge({
        id: 'retry-to-rethink',
        source: 'retry',
        sourceHandle: 'left-source',
        target: 'rethink',
        targetHandle: 'bottom-target',
        label: 'after third retry',
        tone: 'rethink',
        animated: true,
        offset: 52,
      }),
      buildEdge({
        id: 'rethink-to-plan',
        source: 'rethink',
        sourceHandle: 'right-source',
        target: 'plan',
        targetHandle: 'left-target',
        label: 'rethink plan',
        tone: 'rethink',
        animated: true,
        offset: 42,
      }),
    ],
    [],
  );

  return (
    <main className="page-shell">
      <section className="hero-panel" aria-labelledby="diagram-title">
        <div className="ambient ambient-one" />
        <div className="ambient ambient-two" />
        <header className="hero-header">
          <div>
            <SignalBadge tone="brand">Reliant workflow value</SignalBadge>
            <h1 id="diagram-title">One chat becomes an adaptive execution pipeline.</h1>
          </div>
          <p>
            The diagram makes orchestration visible: planning creates parallel work, agents run custom
            loops, review decides pass or fail, retries carry feedback, and rethink paths recover when
            the plan itself needs to change.
          </p>
        </header>

        <section className="flow-shell" aria-label="React Flow workflow diagram">
          <div className="canvas-frame" data-audit="workflow-diagram-react-flow">
            <div className="canvas-caption">
              <SignalBadge tone="parallel">React Flow canvas</SignalBadge>
              <span>Branching, loops, and recovery paths are rendered as connected nodes and edges.</span>
            </div>
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              fitView
              fitViewOptions={{ padding: 0.04 }}
              minZoom={0.65}
              maxZoom={1.15}
              nodesDraggable={false}
              nodesConnectable={false}
              elementsSelectable={false}
              zoomOnDoubleClick={false}
              zoomOnScroll={false}
              panOnScroll={false}
              panOnDrag={false}
              preventScrolling={false}
              proOptions={{ hideAttribution: true }}
            >
              <Background color="rgba(159, 178, 255, 0.18)" gap={36} size={1.35} />
            </ReactFlow>
          </div>
        </section>

        <footer className="capability-strip" aria-label="Capabilities shown">
          {capabilityPillars.map((pillar) => (
            <span key={pillar}>{pillar}</span>
          ))}
        </footer>
      </section>
    </main>
  );
}

createRoot(document.getElementById('root')).render(<App />);
