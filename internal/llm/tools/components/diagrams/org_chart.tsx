import React from 'react';

interface OrgNode {
  name: string;
  title?: string;
  /** Initials for avatar circle */
  initials?: string;
  children?: OrgNode[];
}

interface OrgChartProps {
  /** Root node */
  root: OrgNode;
}

const AVATAR_COLORS = [
  'bg-blue-500',
  'bg-purple-500',
  'bg-emerald-500',
  'bg-amber-500',
  'bg-rose-500',
  'bg-cyan-500',
  'bg-indigo-500',
  'bg-teal-500',
];

function getInitials(node: OrgNode): string {
  if (node.initials) return node.initials;
  return node.name
    .split(/\s+/)
    .map((w) => w[0])
    .filter(Boolean)
    .slice(0, 2)
    .join('')
    .toUpperCase();
}

function getColorClass(depth: number, index: number): string {
  return AVATAR_COLORS[(depth + index) % AVATAR_COLORS.length];
}

function NodeCard({
  node,
  depth,
  index,
}: {
  node: OrgNode;
  depth: number;
  index: number;
}): React.ReactElement {
  const initials = getInitials(node);
  const colorClass = getColorClass(depth, index);

  return (
    <div className="flex flex-col items-center gap-1 px-2">
      {/* Avatar */}
      <div
        className={`w-10 h-10 rounded-full ${colorClass} flex items-center justify-center shadow-sm`}
      >
        <span className="text-xs font-bold text-white leading-none">{initials}</span>
      </div>
      {/* Name */}
      <span className="text-sm font-bold text-gray-800 text-center leading-tight whitespace-nowrap">
        {node.name}
      </span>
      {/* Title */}
      {node.title && (
        <span className="text-[10px] text-gray-500 text-center leading-tight whitespace-nowrap">
          {node.title}
        </span>
      )}
    </div>
  );
}

function SubTree({
  node,
  depth,
  index,
}: {
  node: OrgNode;
  depth: number;
  index: number;
}): React.ReactElement {
  const hasChildren = node.children && node.children.length > 0;

  return (
    <div className="flex flex-col items-center">
      {/* This node's card */}
      <NodeCard node={node} depth={depth} index={index} />

      {hasChildren && (
        <>
          {/* Vertical line down from parent */}
          <div className="w-px h-5 bg-gray-300" />

          {/* Horizontal connector bar spanning all children */}
          {node.children!.length > 1 && (
            <div className="relative flex justify-between" style={{ width: '100%' }}>
              <div
                className="absolute top-0 h-px bg-gray-300"
                style={{
                  left: `calc(${(100 / (node.children!.length * 2))}%)`,
                  right: `calc(${(100 / (node.children!.length * 2))}%)`,
                }}
              />
            </div>
          )}

          {/* Children row */}
          <div className="flex items-start gap-0">
            {node.children!.map((child, ci) => (
              <div key={ci} className="flex flex-col items-center px-3">
                {/* Vertical line down to child */}
                <div className="w-px h-5 bg-gray-300" />
                <SubTree node={child} depth={depth + 1} index={ci} />
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

export default function OrgChart({ root }: OrgChartProps): React.ReactElement {
  return (
    <div className="w-full overflow-x-auto">
      <div className="flex justify-center min-w-max py-4 px-6">
        <SubTree node={root} depth={0} index={0} />
      </div>
    </div>
  );
}
